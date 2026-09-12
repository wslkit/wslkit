//go:build windows

package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/wslkit/wsldoctor/internal/data"
	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/vhdx"
	"github.com/wslkit/wsldoctor/internal/winapi/evtlog"
	"github.com/wslkit/wsldoctor/internal/winapi/fileinfo"
	"github.com/wslkit/wsldoctor/internal/winapi/procs"
	"github.com/wslkit/wsldoctor/internal/winapi/services"
	"github.com/wslkit/wsldoctor/internal/winapi/wmi"
)

const (
	lxssUser = `Software\Microsoft\Windows\CurrentVersion\Lxss`
	lxssMach = `SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss`
)

// Run collects everything. It never invokes wsl.exe unless o.AllowVMWake
// (and, in this version, not even then: no collector needs it yet).
func Run(ctx context.Context, o Options) (*env.Env, error) {
	o = o.withDefaults()
	e := env.New(o.Tool)
	e.Elevated = isElevated()
	e.VMWakeOK = o.AllowVMWake
	e.UserProfile, _ = os.UserHomeDir()
	e.Hostname, _ = os.Hostname()
	e.Host.Arch = hostArch()

	runAll(ctx, e, o, []collector{
		{"os", collectOS},
		{"wmi_system", collectWMISystem},
		{"features", collectFeatures},
		{"services", collectServices},
		{"runtime", collectRuntime},
		{"distros", collectDistros},
		{"config", collectConfig},
		{"defender", collectDefender},
		{"events", collectEvents},
		{"procs", collectProcs},
		{"plugins", collectPlugins},
		{"network_reg", collectNetworkReg},
	})
	return e, nil
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

func hostArch() string {
	if a := os.Getenv("PROCESSOR_ARCHITEW6432"); a != "" {
		return normArch(a)
	}
	if a := os.Getenv("PROCESSOR_ARCHITECTURE"); a != "" {
		return normArch(a)
	}
	return runtime.GOARCH
}

func normArch(a string) string {
	switch strings.ToUpper(a) {
	case "AMD64":
		return "amd64"
	case "ARM64":
		return "arm64"
	case "X86":
		return "386"
	}
	return strings.ToLower(a)
}

func kindOf(err error) env.ErrKind {
	switch {
	case err == nil:
		return env.ErrNone
	case errors.Is(err, registry.ErrNotExist), errors.Is(err, os.ErrNotExist), errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return env.ErrNotPresent
	case errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, os.ErrPermission), errors.Is(err, evtlog.ErrAccessDenied), errors.Is(err, procs.ErrAccessDenied):
		return env.ErrNeedsElevation
	case errors.Is(err, context.DeadlineExceeded):
		return env.ErrTimeout
	}
	return env.ErrOther
}

// ---------------------------------------------------------------- os

func collectOS(ctx context.Context, e *env.Env, o Options) error {
	const src = `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.READ)
	if err != nil {
		e.Host.OS = env.Fail[env.OSBuild](kindOf(err), src, err)
		return err
	}
	defer k.Close()
	var b env.OSBuild
	if v, _, err := k.GetIntegerValue("CurrentMajorVersionNumber"); err == nil {
		b.Major = int(v)
	}
	if v, _, err := k.GetIntegerValue("CurrentMinorVersionNumber"); err == nil {
		b.Minor = int(v)
	}
	if v, _, err := k.GetStringValue("CurrentBuildNumber"); err == nil {
		b.Build, _ = strconv.Atoi(v)
	}
	if v, _, err := k.GetIntegerValue("UBR"); err == nil {
		b.UBR = int(v)
	}
	b.ProductName, _, _ = k.GetStringValue("ProductName")
	b.DisplayVersion, _, _ = k.GetStringValue("DisplayVersion")
	b.EditionID, _, _ = k.GetStringValue("EditionID")
	e.Host.OS = env.Ok(b, src)

	// Pending reboot markers.
	pending := false
	for _, p := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`,
	} {
		if pk, err := registry.OpenKey(registry.LOCAL_MACHINE, p, registry.READ); err == nil {
			pk.Close()
			pending = true
		}
	}
	e.Host.PendingReboot = env.Ok(pending, "HKLM CBS RebootPending / WU RebootRequired")
	return nil
}

// ---------------------------------------------------------------- wmi

func collectWMISystem(ctx context.Context, e *env.Env, o Options) error {
	const src = "Win32_ComputerSystem"
	rows, err := wmi.Query(ctx, `root\cimv2`, "SELECT HypervisorPresent, TotalPhysicalMemory FROM Win32_ComputerSystem", "HypervisorPresent", "TotalPhysicalMemory")
	if err != nil || len(rows) == 0 {
		if err == nil {
			err = errors.New("no rows")
		}
		e.Host.HypervisorPresent = env.Fail[bool](kindOf(err), src, err)
		e.Host.TotalMemoryBytes = env.Fail[uint64](kindOf(err), src, err)
		return err
	}
	if hv, ok := rows[0]["HypervisorPresent"].(bool); ok {
		e.Host.HypervisorPresent = env.Ok(hv, src)
	} else {
		e.Host.HypervisorPresent = env.Fail[bool](env.ErrOther, src, errors.New("HypervisorPresent missing"))
	}
	if s := fmt.Sprint(rows[0]["TotalPhysicalMemory"]); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			e.Host.TotalMemoryBytes = env.Ok(n, src)
		}
	}
	return nil
}

func collectFeatures(ctx context.Context, e *env.Env, o Options) error {
	const src = "Win32_OptionalFeature"
	rows, err := wmi.Query(ctx, `root\cimv2`,
		"SELECT Name, InstallState FROM Win32_OptionalFeature WHERE Name='Microsoft-Windows-Subsystem-Linux' OR Name='VirtualMachinePlatform' OR Name='Microsoft-Hyper-V-All' OR Name='Microsoft-Hyper-V' OR Name='HypervisorPlatform'",
		"Name", "InstallState")
	if err != nil {
		e.Host.Features = env.Fail[map[string]int](kindOf(err), src, err)
		return err
	}
	m := map[string]int{}
	for _, r := range rows {
		name := fmt.Sprint(r["Name"])
		st, _ := strconv.Atoi(fmt.Sprint(r["InstallState"]))
		m[name] = st
	}
	e.Host.Features = env.Ok(m, src)
	return nil
}

// ---------------------------------------------------------------- services

func collectServices(ctx context.Context, e *env.Env, o Options) error {
	const src = "Service Control Manager"
	m, err := services.Query([]string{"WSLService", "LxssManager", "vmcompute", "HvHost", "vmms", "WslInstaller", "vmx86", "VBoxSup", "SharedAccess"})
	if err != nil {
		e.Host.Services = env.Fail[map[string]env.Service](kindOf(err), src, err)
		return err
	}
	out := make(map[string]env.Service, len(m))
	for k, v := range m {
		out[k] = env.Service{Exists: v.Exists, State: v.State, StartType: v.StartType, Display: v.Display}
	}
	e.Host.Services = env.Ok(out, src)
	return nil
}

// ---------------------------------------------------------------- runtime

func collectRuntime(ctx context.Context, e *env.Env, o Options) error {
	// Install location: HKLM Lxss\MSI, falling back to Program Files\WSL.
	loc := ""
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, lxssMach+`\MSI`, registry.READ); err == nil {
		loc, _, _ = k.GetStringValue("InstallLocation")
		if v, _, err := k.GetStringValue("Version"); err == nil {
			e.Runtime.MSIVersion = env.Ok(v, `HKLM\...\Lxss\MSI\Version`)
		}
		k.Close()
	}
	if loc == "" {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		loc = filepath.Join(pf, "WSL")
	}
	svcPath := filepath.Join(loc, "wslservice.exe")
	if v, err := fileinfo.Version(svcPath); err == nil {
		e.Runtime.Version = env.Ok(v, svcPath+" file version")
		e.Runtime.InstallLocation = env.Ok(loc, `HKLM\...\Lxss\MSI\InstallLocation`)
	} else {
		e.Runtime.Version = env.Fail[string](kindOf(err), svcPath, err)
		e.Runtime.InstallLocation = env.Fail[string](kindOf(err), svcPath, err)
	}

	inbox := filepath.Join(os.Getenv("SystemRoot"), "System32", "wsl.exe")
	if v, err := fileinfo.Version(inbox); err == nil {
		e.Runtime.InboxWslVersion = env.Ok(v, inbox)
	} else {
		e.Runtime.InboxWslVersion = env.Fail[string](kindOf(err), inbox, err)
	}

	// Appx package full name.
	const appxSrc = `HKCU\Software\Classes\Local Settings\...\AppModel\Repository\Packages`
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`, registry.READ); err == nil {
		subs, _ := k.ReadSubKeyNames(0)
		k.Close()
		found := ""
		for _, s := range subs {
			if strings.HasPrefix(s, "MicrosoftCorporationII.WindowsSubsystemForLinux_") {
				found = s
			}
		}
		if found != "" {
			e.Runtime.AppxFullName = env.Ok(found, appxSrc)
		} else {
			e.Runtime.AppxFullName = env.Absent[string](appxSrc)
		}
	} else {
		e.Runtime.AppxFullName = env.Fail[string](kindOf(err), appxSrc, err)
	}

	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, lxssMach, registry.READ); err == nil {
		if v, _, err := k.GetStringValue("KernelVersion"); err == nil {
			e.Runtime.KernelVersion = env.Ok(v, `HKLM\...\Lxss\KernelVersion`)
		}
		if v, _, err := k.GetStringValue("NatNetwork"); err == nil {
			e.Runtime.NatNetwork = env.Ok(v, `HKLM\...\Lxss\NatNetwork`)
		}
		k.Close()
	}

	if c, err := data.LoadCompat(); err == nil {
		e.Runtime.LatestStable = env.Ok(c.LatestStable, "embedded compat.json "+c.Updated)
		e.Runtime.LatestStableSource = "embedded data " + c.Updated
	} else {
		e.Runtime.LatestStable = env.Fail[string](env.ErrOther, "embedded compat.json", err)
	}
	return nil
}

// ---------------------------------------------------------------- distros

func collectDistros(ctx context.Context, e *env.Env, o Options) error {
	src := `HKCU\` + lxssUser
	k, err := registry.OpenKey(registry.CURRENT_USER, lxssUser, registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			e.Distros = env.Absent[[]env.Distro](src)
			return nil
		}
		e.Distros = env.Fail[[]env.Distro](kindOf(err), src, err)
		return err
	}
	defer k.Close()
	def, _, _ := k.GetStringValue("DefaultDistribution")
	subs, err := k.ReadSubKeyNames(0)
	if err != nil {
		e.Distros = env.Fail[[]env.Distro](kindOf(err), src, err)
		return err
	}
	var out []env.Distro
	for _, guid := range subs {
		if !strings.HasPrefix(guid, "{") {
			continue
		}
		dk, err := registry.OpenKey(k, guid, registry.READ)
		if err != nil {
			continue
		}
		d := env.Distro{GUID: guid, IsDefault: strings.EqualFold(guid, def)}
		d.Name, _, _ = dk.GetStringValue("DistributionName")
		d.BasePath, _, _ = dk.GetStringValue("BasePath")
		d.VhdFileName, _, _ = dk.GetStringValue("VhdFileName")
		d.Flavor, _, _ = dk.GetStringValue("Flavor")
		d.OsVersion, _, _ = dk.GetStringValue("OsVersion")
		d.Version = intValue(dk, "Version")
		d.State = intValue(dk, "State")
		d.Flags = intValue(dk, "Flags")
		d.DefaultUid = intValue(dk, "DefaultUid")
		d.RunOOBE = intValue(dk, "RunOOBE")
		d.Modern = intValue(dk, "Modern")
		d.ValueNames, _ = dk.ReadValueNames(0)
		dk.Close()
		d.Running = env.Fail[bool](env.ErrVMWakeRefused, "wsl --list --running", errors.New("not queried: would invoke wsl.exe"))
		if d.Version == 2 && d.BasePath != "" {
			vhdName := d.VhdFileName
			if vhdName == "" {
				vhdName = "ext4.vhdx"
			}
			base := strings.TrimPrefix(d.BasePath, `\\?\`)
			d.Vhd = inspectVhd(filepath.Join(base, vhdName))
			if free, err := fileinfo.VolumeFree(base); err == nil {
				d.VolumeFree = env.Ok(free, "GetDiskFreeSpaceEx "+filepath.VolumeName(base))
			} else {
				d.VolumeFree = env.Fail[uint64](kindOf(err), base, err)
			}
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		e.Distros = env.Ok([]env.Distro{}, src)
		return nil
	}
	e.Distros = env.Ok(out, src)
	return nil
}

func intValue(k registry.Key, name string) int {
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return -1
	}
	return int(v)
}

func inspectVhd(path string) env.Field[env.VhdInfo] {
	info := env.VhdInfo{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return env.Fail[env.VhdInfo](kindOf(err), path, err)
	}
	info.FileSize = uint64(fi.Size())
	if a, err := fileinfo.Attributes(path); err == nil {
		info.Sparse, info.Compressed, info.Encrypted = a.Sparse, a.Compressed, a.Encrypted
	}
	if sid, err := fileinfo.OwnerSID(path); err == nil {
		info.OwnerSID = sid
	}
	h, err := fileinfo.OpenShared(path)
	if err != nil {
		info.ParseErr = "open: " + err.Error()
		if kindOf(err) == env.ErrNeedsElevation {
			return env.Fail[env.VhdInfo](env.ErrNeedsElevation, path, err)
		}
		return env.Ok(info, path) // stat worked; parse did not
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	parsed, perr := vhdx.Parse(f, fi.Size())
	if parsed != nil {
		info.MagicOK = parsed.MagicOK
		info.HeaderOK = parsed.HeaderOK
		info.VirtualSize = parsed.VirtualSize
		info.BlockSize = parsed.BlockSize
		info.AllocatedBytes = parsed.AllocatedBytes
	}
	if perr != nil {
		info.ParseErr = perr.Error()
	}
	return env.Ok(info, path)
}

// ---------------------------------------------------------------- config

func collectConfig(ctx context.Context, e *env.Env, o Options) error {
	home, err := os.UserHomeDir()
	if err != nil {
		e.Config.WslConfig = env.Fail[string](env.ErrOther, "%USERPROFILE%\\.wslconfig", err)
		return err
	}
	p := filepath.Join(home, ".wslconfig")
	e.Config.WslConfigPath = p
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		e.Config.WslConfig = env.Ok(string(b), p)
	case errors.Is(err, os.ErrNotExist):
		e.Config.WslConfig = env.Absent[string](p)
	default:
		e.Config.WslConfig = env.Fail[string](kindOf(err), p, err)
	}
	return nil
}

// ---------------------------------------------------------------- defender

func collectDefender(ctx context.Context, e *env.Env, o Options) error {
	const ns = `root\microsoft\windows\defender`
	status, err := wmi.Query(ctx, ns, "SELECT AMRunningMode, RealTimeProtectionEnabled FROM MSFT_MpComputerStatus", "AMRunningMode", "RealTimeProtectionEnabled")
	if err != nil || len(status) == 0 {
		if err == nil {
			err = errors.New("no MSFT_MpComputerStatus rows")
		}
		// Namespace missing means Defender is not present (Server Core, third-party AV removed it).
		e.Defender.Present = env.Absent[bool]("MSFT_MpComputerStatus: " + err.Error())
		return nil
	}
	e.Defender.Present = env.Ok(true, "MSFT_MpComputerStatus")
	if mode := fmt.Sprint(status[0]["AMRunningMode"]); mode != "" {
		e.Defender.RunningMode = env.Ok(mode, "MSFT_MpComputerStatus.AMRunningMode")
	}
	if rt, ok := status[0]["RealTimeProtectionEnabled"].(bool); ok {
		e.Defender.RealtimeEnabled = env.Ok(rt, "MSFT_MpComputerStatus.RealTimeProtectionEnabled")
	}
	if strings.EqualFold(fmt.Sprint(status[0]["AMRunningMode"]), "Passive Mode") || strings.EqualFold(fmt.Sprint(status[0]["AMRunningMode"]), "Not running") {
		e.Defender.Present = env.Ok(false, "MSFT_MpComputerStatus.AMRunningMode="+fmt.Sprint(status[0]["AMRunningMode"]))
	}

	pref, err := wmi.Query(ctx, ns, "SELECT ExclusionPath, ExclusionProcess, ExclusionExtension FROM MSFT_MpPreference", "ExclusionPath", "ExclusionProcess", "ExclusionExtension")
	if err != nil || len(pref) == 0 {
		if err == nil {
			err = errors.New("no MSFT_MpPreference rows")
		}
		e.Defender.Exclusions = env.Fail[env.Exclusions](kindOf(err), "MSFT_MpPreference", err)
		return nil
	}
	ex := env.Exclusions{
		Paths:      wmi.Strings(pref[0]["ExclusionPath"]),
		Processes:  wmi.Strings(pref[0]["ExclusionProcess"]),
		Extensions: wmi.Strings(pref[0]["ExclusionExtension"]),
	}
	for _, p := range ex.Paths {
		if strings.Contains(p, "Must be an administrator") {
			e.Defender.Exclusions = env.Fail[env.Exclusions](env.ErrNeedsElevation, "MSFT_MpPreference.ExclusionPath", env.ErrNeedsElevationSentinel)
			return nil
		}
	}
	e.Defender.Exclusions = env.Ok(ex, "MSFT_MpPreference")
	return nil
}

// ---------------------------------------------------------------- events

func collectEvents(ctx context.Context, e *env.Env, o Options) error {
	e.Events.Window = o.EventWindow
	for _, ch := range []string{
		"Microsoft-Windows-Hyper-V-VmSwitch-Operational",
		"Microsoft-Windows-Hyper-V-Compute-Admin",
		"Microsoft-Windows-Host-Network-Service-Admin",
		"System", "Application",
	} {
		n, err := evtlog.RecordCount(ch)
		if err != nil {
			e.Events.Channels[ch] = env.Fail[env.ChannelInfo](kindOf(err), ch, err)
			continue
		}
		e.Events.Channels[ch] = env.Ok(env.ChannelInfo{Records: n}, ch)
	}
	since := evtlog.SinceMillis(o.EventWindow)
	var recent []env.Event
	add := func(channel string, evs []evtlog.Event, keep func(evtlog.Event) bool) {
		for _, ev := range evs {
			if keep != nil && !keep(ev) {
				continue
			}
			recent = append(recent, env.Event{Channel: channel, Provider: ev.Provider, ID: ev.ID, Level: ev.Level, Time: ev.Time, Message: ev.Message})
		}
	}
	var firstErr error
	// WER / Application Error mentioning WSL binaries.
	evs, err := evtlog.Query("Application", `*[System[(Provider[@Name='Application Error'] or Provider[@Name='Windows Error Reporting']) and `+since+`]]`, 200)
	if err != nil {
		firstErr = err
	}
	add("Application", evs, func(ev evtlog.Event) bool {
		m := strings.ToLower(ev.Message)
		return strings.Contains(m, "wsl") || strings.Contains(m, "vmmem") || strings.Contains(m, "lxss")
	})
	// Sleep / resume.
	evs, err = evtlog.Query("System", `*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and (EventID=42 or EventID=107) and `+since+`]]`, 20)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	add("System", evs, nil)
	// VmSwitch errors/critical.
	evs, err = evtlog.Query("Microsoft-Windows-Hyper-V-VmSwitch-Operational", `*[System[Level<=2 and `+since+`]]`, 20)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	add("Microsoft-Windows-Hyper-V-VmSwitch-Operational", evs, nil)

	if firstErr != nil && len(recent) == 0 {
		e.Events.Recent = env.Fail[[]env.Event](kindOf(firstErr), "wevtapi", firstErr)
		return firstErr
	}
	if recent == nil {
		recent = []env.Event{}
	}
	e.Events.Recent = env.Ok(recent, "wevtapi Application/System/VmSwitch")
	return nil
}

// ---------------------------------------------------------------- procs

func collectProcs(ctx context.Context, e *env.Env, o Options) error {
	found, err := procs.Find("vmmemWSL", "vmmem", "wslservice.exe")
	if err != nil {
		e.Procs.VmmemWorkingSet = env.Fail[uint64](kindOf(err), "Toolhelp32", err)
		return err
	}
	e.Procs.WslServiceRunning = env.Ok(len(found["wslservice.exe"]) > 0, "Toolhelp32")
	for _, name := range []string{"vmmemWSL", "vmmem"} {
		pids := found[name]
		if len(pids) == 0 {
			continue
		}
		e.Procs.VmmemName = name
		ws, err := procs.WorkingSet(pids[0])
		if err != nil {
			e.Procs.VmmemWorkingSet = env.Fail[uint64](kindOf(err), name, err)
			return nil
		}
		e.Procs.VmmemWorkingSet = env.Ok(ws, name+" working set")
		return nil
	}
	e.Procs.VmmemWorkingSet = env.Absent[uint64]("vmmemWSL/vmmem not running")
	return nil
}

// ---------------------------------------------------------------- plugins

func collectPlugins(ctx context.Context, e *env.Env, o Options) error {
	src := `HKLM\` + lxssMach + `\Plugins`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, lxssMach+`\Plugins`, registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			e.Plugins = env.Absent[[]env.Plugin](src)
			return nil
		}
		e.Plugins = env.Fail[[]env.Plugin](kindOf(err), src, err)
		return err
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil {
		e.Plugins = env.Fail[[]env.Plugin](kindOf(err), src, err)
		return err
	}
	out := []env.Plugin{}
	for _, n := range names {
		p, _, err := k.GetStringValue(n)
		if err != nil {
			continue
		}
		pl := env.Plugin{Name: n, Path: p}
		if _, err := os.Stat(p); err == nil {
			pl.Exists = true
			pl.Version, _ = fileinfo.Version(p)
		}
		out = append(out, pl)
	}
	e.Plugins = env.Ok(out, src)
	return nil
}

// ---------------------------------------------------------------- network registry facts

func collectNetworkReg(ctx context.Context, e *env.Env, o Options) error {
	const src = `HKLM\SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters\DisabledComponents`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters`, registry.READ)
	if err != nil {
		e.Host.IPv6Disabled = env.Fail[uint32](kindOf(err), src, err)
		return err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("DisabledComponents")
	if err != nil {
		e.Host.IPv6Disabled = env.Ok(uint32(0), src+" (not set)")
		return nil
	}
	e.Host.IPv6Disabled = env.Ok(uint32(v), src)
	return nil
}

var _ = time.Second
