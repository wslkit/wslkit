//go:build windows

// Spike S1–S4: can the data wsldoctor needs be read unelevated, without
// PowerShell, without waking the WSL VM? Prints findings and timings.
// Throwaway code; results are recorded in docs/decisions/.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func main() {
	fmt.Printf("elevated: %v\n\n", isElevated())
	section("S1 WMI root/cimv2", func() {
		wmi(`root\cimv2`, `SELECT Name, InstallState FROM Win32_OptionalFeature WHERE Name='Microsoft-Windows-Subsystem-Linux' OR Name='VirtualMachinePlatform' OR Name='Microsoft-Hyper-V-All' OR Name='Microsoft-Hyper-V'`, "Name", "InstallState")
		wmi(`root\cimv2`, `SELECT HypervisorPresent, TotalPhysicalMemory, Manufacturer FROM Win32_ComputerSystem`, "HypervisorPresent", "TotalPhysicalMemory", "Manufacturer")
		wmi(`root\cimv2`, `SELECT Name, State, StartMode FROM Win32_Service WHERE Name='LxssManager' OR Name='wslservice' OR Name='vmcompute' OR Name='HvHost' OR Name='WslInstaller'`, "Name", "State", "StartMode")
	})
	section("S3 WMI Defender", func() {
		wmi(`root\microsoft\windows\defender`, `SELECT ExclusionPath, ExclusionProcess, DisableRealtimeMonitoring FROM MSFT_MpPreference`, "ExclusionPath", "ExclusionProcess", "DisableRealtimeMonitoring")
		wmi(`root\microsoft\windows\defender`, `SELECT AMRunningMode, RealTimeProtectionEnabled, AMProductVersion FROM MSFT_MpComputerStatus`, "AMRunningMode", "RealTimeProtectionEnabled", "AMProductVersion")
		regRead(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows Defender\Exclusions\Paths`)
		regRead(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows Defender\Exclusions\Paths`)
	})
	section("S2 registry + versions", func() {
		regLxss()
		fileVersion(`C:\Program Files\WSL\wslservice.exe`)
		fileVersion(`C:\Program Files\WSL\wsl.exe`)
		fileVersion(`C:\Windows\System32\wsl.exe`)
		appxPackages()
	})
	section("S4 event log channels", func() {
		for _, ch := range []string{
			"Microsoft-Windows-Hyper-V-VmSwitch-Operational",
			"Microsoft-Windows-Hyper-V-Compute-Admin",
			"Microsoft-Windows-Hyper-V-Compute-Operational",
			"Microsoft-Windows-Hyper-V-Worker-Admin",
			"Microsoft-Windows-Host-Network-Service-Admin",
			"Microsoft-Windows-Host-Network-Service-Operational",
			"Microsoft-Windows-Kernel-Power/Thermal-Operational",
			"System",
			"Application",
		} {
			evt(ch)
		}
		evtQuery("Application", `*[System[Provider[@Name='Windows Error Reporting' or @Name='Application Error']]]`)
		evtQuery("System", `*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and (EventID=107 or EventID=42)]]`)
	})
	section("S2b .wslconfig / temp logs", func() {
		home, _ := os.UserHomeDir()
		stat(filepath.Join(home, ".wslconfig"))
		stat(filepath.Join(os.Getenv("TEMP"), "wsl-crashes"))
		stat(filepath.Join(os.Getenv("TEMP"), "wsl-install-logs.txt"))
		stat(`C:\Windows\temp\wsl-install-log.txt`)
	})
}

func section(name string, f func()) {
	fmt.Printf("=== %s ===\n", name)
	f()
	fmt.Println()
}

func timed(label string, f func() error) {
	t := time.Now()
	err := f()
	d := time.Since(t).Round(time.Millisecond)
	if err != nil {
		fmt.Printf("  [%6v] %s: ERROR %v\n", d, label, err)
		return
	}
	fmt.Printf("  [%6v] %s: ok\n", d, label)
}

func isElevated() bool {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID, windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid); err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	tok := windows.GetCurrentProcessToken()
	member, _ := tok.IsMember(sid)
	return member && tok.IsElevated()
}

// ---- WMI via late-bound COM ----

func wmi(namespace, query string, fields ...string) {
	timed(fmt.Sprintf("%s :: %s", namespace, short(query)), func() error {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
			if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != 0x00000001 { // S_FALSE = already init
				return fmt.Errorf("CoInitializeEx: %w", err)
			}
		}
		defer ole.CoUninitialize()
		unk, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
		if err != nil {
			return fmt.Errorf("CreateObject: %w", err)
		}
		defer unk.Release()
		loc, err := unk.QueryInterface(ole.IID_IDispatch)
		if err != nil {
			return err
		}
		defer loc.Release()
		svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", ".", namespace)
		if err != nil {
			return fmt.Errorf("ConnectServer: %w", err)
		}
		svc := svcRaw.ToIDispatch()
		defer svc.Release()
		resRaw, err := oleutil.CallMethod(svc, "ExecQuery", query)
		if err != nil {
			return fmt.Errorf("ExecQuery: %w", err)
		}
		res := resRaw.ToIDispatch()
		defer res.Release()
		countV, err := oleutil.GetProperty(res, "Count")
		if err != nil {
			return fmt.Errorf("Count: %w", err)
		}
		n := int(countV.Val)
		for i := 0; i < n; i++ {
			itemRaw, err := oleutil.CallMethod(res, "ItemIndex", i)
			if err != nil {
				return fmt.Errorf("ItemIndex: %w", err)
			}
			item := itemRaw.ToIDispatch()
			var parts []string
			for _, f := range fields {
				v, err := oleutil.GetProperty(item, f)
				if err != nil {
					parts = append(parts, f+"=<err>")
					continue
				}
				parts = append(parts, fmt.Sprintf("%s=%v", f, variantString(v)))
				v.Clear()
			}
			item.Release()
			fmt.Printf("      %s\n", strings.Join(parts, "  "))
		}
		if n == 0 {
			fmt.Println("      (no rows)")
		}
		return nil
	})
}

func variantString(v *ole.VARIANT) string {
	if v.VT&ole.VT_ARRAY != 0 {
		arr := v.ToArray()
		if arr == nil {
			return "[]"
		}
		vals := arr.ToValueArray()
		return fmt.Sprintf("%v", vals)
	}
	return fmt.Sprintf("%v", v.Value())
}

func short(q string) string {
	i := strings.Index(q, "FROM ")
	if i < 0 {
		return q
	}
	rest := q[i+5:]
	if j := strings.Index(rest, " "); j > 0 {
		rest = rest[:j]
	}
	return rest
}

// ---- Registry ----

func regRead(root registry.Key, path string) {
	timed("reg "+path, func() error {
		k, err := registry.OpenKey(root, path, registry.READ)
		if err != nil {
			return err
		}
		defer k.Close()
		names, err := k.ReadValueNames(0)
		if err != nil {
			return err
		}
		fmt.Printf("      %d values\n", len(names))
		return nil
	})
}

func regLxss() {
	timed("HKCU Lxss distro inventory", func() error {
		k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Lxss`, registry.READ)
		if err != nil {
			return err
		}
		defer k.Close()
		def, _, _ := k.GetStringValue("DefaultDistribution")
		fmt.Printf("      DefaultDistribution=%s\n", def)
		subs, err := k.ReadSubKeyNames(0)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if !strings.HasPrefix(s, "{") {
				continue
			}
			d, err := registry.OpenKey(k, s, registry.READ)
			if err != nil {
				return err
			}
			name, _, _ := d.GetStringValue("DistributionName")
			base, _, _ := d.GetStringValue("BasePath")
			ver, _, _ := d.GetIntegerValue("Version")
			state, _, _ := d.GetIntegerValue("State")
			flags, _, _ := d.GetIntegerValue("Flags")
			uid, _, _ := d.GetIntegerValue("DefaultUid")
			modern, _, _ := d.GetIntegerValue("Modern")
			oobe, _, _ := d.GetIntegerValue("RunOOBE")
			flavor, _, _ := d.GetStringValue("Flavor")
			osv, _, _ := d.GetStringValue("OsVersion")
			vals, _ := d.ReadValueNames(0)
			d.Close()
			fmt.Printf("      %s name=%s ver=%d state=%d flags=%d uid=%d modern=%d oobe=%d flavor=%q osver=%q\n        values=%v\n        base=%s\n",
				s, name, ver, state, flags, uid, modern, oobe, flavor, osv, vals, redact(base))
			stat(filepath.Join(strings.TrimPrefix(base, `\\?\`), "ext4.vhdx"))
		}
		return nil
	})
	timed("HKLM Lxss", func() error {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss`, registry.READ)
		if err != nil {
			return err
		}
		defer k.Close()
		vals, _ := k.ReadValueNames(0)
		subs, _ := k.ReadSubKeyNames(0)
		fmt.Printf("      values=%v subkeys=%v\n", vals, subs)
		for _, v := range vals {
			if s, _, err := k.GetStringValue(v); err == nil {
				fmt.Printf("      %s=%s\n", v, s)
			} else if n, _, err := k.GetIntegerValue(v); err == nil {
				fmt.Printf("      %s=%d\n", v, n)
			}
		}
		for _, s := range subs {
			sk, err := registry.OpenKey(k, s, registry.READ)
			if err != nil {
				fmt.Printf("      %s: %v\n", s, err)
				continue
			}
			sv, _ := sk.ReadValueNames(0)
			ss, _ := sk.ReadSubKeyNames(0)
			fmt.Printf("      %s: values=%v subkeys=%v\n", s, sv, ss)
			sk.Close()
		}
		return nil
	})
	regRead(registry.CLASSES_ROOT, `CLSID\{e66b0f30-e7b4-4f8c-acfd-d100c46c6278}`)
	regRead(registry.CLASSES_ROOT, `CLSID\{a9b7a1b9-0671-405c-95f1-e0612cb4ce7e}`)
	regRead(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\P9NP`)
	regRead(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\WinSock2\Parameters\Protocol_Catalog9\Catalog_Entries`)
	timed("Tcpip6 DisabledComponents", func() error {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters`, registry.READ)
		if err != nil {
			return err
		}
		defer k.Close()
		v, _, err := k.GetIntegerValue("DisabledComponents")
		if err != nil {
			fmt.Println("      not set")
			return nil
		}
		fmt.Printf("      DisabledComponents=0x%x\n", v)
		return nil
	})
}

func redact(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" {
		return strings.ReplaceAll(p, home, "%USERPROFILE%")
	}
	return p
}

// ---- File version ----

func fileVersion(path string) {
	timed("version "+path, func() error {
		if _, err := os.Stat(path); err != nil {
			return err
		}
		size, err := windows.GetFileVersionInfoSize(path, nil)
		if err != nil {
			return err
		}
		buf := make([]byte, size)
		if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
			return err
		}
		var fixed *windows.VS_FIXEDFILEINFO
		var fixedLen uint32
		if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &fixedLen); err != nil {
			return err
		}
		fmt.Printf("      file=%d.%d.%d.%d product=%d.%d.%d.%d\n",
			fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16, fixed.FileVersionLS&0xffff,
			fixed.ProductVersionMS>>16, fixed.ProductVersionMS&0xffff, fixed.ProductVersionLS>>16, fixed.ProductVersionLS&0xffff)
		return nil
	})
}

func appxPackages() {
	timed("Appx repository (HKCU Classes)", func() error {
		k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`, registry.READ)
		if err != nil {
			return err
		}
		defer k.Close()
		subs, err := k.ReadSubKeyNames(0)
		if err != nil {
			return err
		}
		n := 0
		for _, s := range subs {
			if strings.HasPrefix(s, "MicrosoftCorporationII.WindowsSubsystemForLinux") || strings.Contains(s, "Ubuntu") || strings.HasPrefix(s, "CanonicalGroupLimited") {
				fmt.Printf("      %s\n", s)
				n++
			}
		}
		fmt.Printf("      (%d of %d packages matched)\n", n, len(subs))
		return nil
	})
	timed("Appx (HKLM ...\\AppModel\\StateRepository / PackageRepository)", func() error {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Appx\AppxAllUserStore\Applications`, registry.READ)
		if err != nil {
			return err
		}
		defer k.Close()
		subs, _ := k.ReadSubKeyNames(0)
		for _, s := range subs {
			if strings.HasPrefix(s, "MicrosoftCorporationII.WindowsSubsystemForLinux") {
				fmt.Printf("      %s\n", s)
			}
		}
		return nil
	})
}

// ---- Event log ----

var (
	modWevtapi        = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtQuery      = modWevtapi.NewProc("EvtQuery")
	procEvtNext       = modWevtapi.NewProc("EvtNext")
	procEvtClose      = modWevtapi.NewProc("EvtClose")
	procEvtOpenLog    = modWevtapi.NewProc("EvtOpenLog")
	procEvtGetLogInfo = modWevtapi.NewProc("EvtGetLogInfo")
)

const (
	evtQueryChannelPath      = 0x1
	evtQueryReverseDirection = 0x200
	evtOpenChannelPath       = 0x1
	evtLogNumberOfLogRecords = 6 // EvtLogNumberOfLogRecords
)

func evt(channel string) {
	timed("channel "+channel, func() error {
		p, _ := windows.UTF16PtrFromString(channel)
		h, _, e := procEvtOpenLog.Call(0, uintptr(unsafe.Pointer(p)), evtOpenChannelPath)
		if h == 0 {
			return e
		}
		defer procEvtClose.Call(h)
		// EvtGetLogInfo -> EVT_VARIANT (24 bytes): value(8) count(4) type(4) ... we read UInt64 at offset 0
		var buf [24]byte
		var used uint32
		r, _, e := procEvtGetLogInfo.Call(h, evtLogNumberOfLogRecords, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)))
		if r == 0 {
			return fmt.Errorf("EvtGetLogInfo: %v", e)
		}
		n := *(*uint64)(unsafe.Pointer(&buf[0]))
		fmt.Printf("      records=%d\n", n)
		return nil
	})
}

func evtQuery(channel, xpath string) {
	timed(fmt.Sprintf("query %s :: %s", channel, xpath), func() error {
		cp, _ := windows.UTF16PtrFromString(channel)
		qp, _ := windows.UTF16PtrFromString(xpath)
		h, _, e := procEvtQuery.Call(0, uintptr(unsafe.Pointer(cp)), uintptr(unsafe.Pointer(qp)), evtQueryChannelPath|evtQueryReverseDirection)
		if h == 0 {
			return e
		}
		defer procEvtClose.Call(h)
		var events [10]uintptr
		var returned uint32
		r, _, e := procEvtNext.Call(h, uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), 1000, 0, uintptr(unsafe.Pointer(&returned)))
		if r == 0 {
			if e == windows.ERROR_NO_MORE_ITEMS {
				fmt.Println("      0 matching events")
				return nil
			}
			return fmt.Errorf("EvtNext: %v", e)
		}
		for i := 0; i < int(returned); i++ {
			procEvtClose.Call(events[i])
		}
		fmt.Printf("      >= %d matching events (newest first)\n", returned)
		return nil
	})
}

func stat(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		fmt.Printf("      stat %s: %v\n", redact(path), err)
		return
	}
	fmt.Printf("      stat %s: %d bytes, mod %s\n", redact(path), fi.Size(), fi.ModTime().Format(time.RFC3339))
}
