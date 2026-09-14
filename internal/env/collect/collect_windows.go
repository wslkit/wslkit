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
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	agentconfig "github.com/wslkit/wslkit/internal/agent/config"
	agenthost "github.com/wslkit/wslkit/internal/agent/host"
	agentinstall "github.com/wslkit/wslkit/internal/agent/install"
	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/ext4"
	"github.com/wslkit/wslkit/internal/peexport"
	"github.com/wslkit/wslkit/internal/release"
	"github.com/wslkit/wslkit/internal/vhdx"
	"github.com/wslkit/wslkit/internal/winapi/authenticode"
	"github.com/wslkit/wslkit/internal/winapi/evtlog"
	"github.com/wslkit/wslkit/internal/winapi/fileinfo"
	"github.com/wslkit/wslkit/internal/winapi/netinfo"
	"github.com/wslkit/wslkit/internal/winapi/procs"
	"github.com/wslkit/wslkit/internal/winapi/services"
	"github.com/wslkit/wslkit/internal/winapi/wmi"
	"github.com/wslkit/wslkit/internal/wslconfig"
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
		{"adapters", collectAdapters},
		{"firewall", collectFirewall},
		{"hotfixes", collectHotfixes},
		{"policy", collectPolicy},
		{"agent", collectAgent},
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
	const wql = "SELECT Name, InstallState FROM Win32_OptionalFeature WHERE Name='Microsoft-Windows-Subsystem-Linux' OR Name='VirtualMachinePlatform' OR Name='Microsoft-Hyper-V-All' OR Name='Microsoft-Hyper-V' OR Name='HypervisorPlatform'"
	var m map[string]int
	var err error
	// A cold WMI repository has been observed to answer with a partial list on
	// the first query (CI runners, ADR 0007 follow-up). Retry once when the key
	// feature is missing.
	for attempt := 0; attempt < 2; attempt++ {
		var rows []wmi.Row
		rows, err = wmi.Query(ctx, `root\cimv2`, wql, "Name", "InstallState")
		if err != nil {
			break
		}
		m = map[string]int{}
		for _, r := range rows {
			name := fmt.Sprint(r["Name"])
			st, _ := strconv.Atoi(fmt.Sprint(r["InstallState"]))
			m[name] = st
		}
		if _, ok := m["VirtualMachinePlatform"]; ok {
			break
		}
	}
	if err != nil {
		e.Host.Features = env.Fail[map[string]int](kindOf(err), src, err)
		return err
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

	// COM registration of the Store/MSI service class (CLSID_LxssUserSession).
	const clsid = `CLSID\{a9b7a1b9-0671-405c-95f1-e0612cb4ce7e}`
	if k, err := registry.OpenKey(registry.CLASSES_ROOT, clsid, registry.READ); err == nil {
		k.Close()
		e.Runtime.COMClassRegistered = env.Ok(true, `HKCR\`+clsid)
	} else if errors.Is(err, registry.ErrNotExist) {
		e.Runtime.COMClassRegistered = env.Ok(false, `HKCR\`+clsid)
	} else {
		e.Runtime.COMClassRegistered = env.Fail[bool](kindOf(err), `HKCR\`+clsid, err)
	}

	if c, err := data.LoadCompat(); err == nil {
		e.Runtime.LatestStable = env.Ok(c.LatestStable, "embedded compat.json "+c.Updated)
		e.Runtime.LatestStableSource = "embedded data " + c.Updated
	} else {
		e.Runtime.LatestStable = env.Fail[string](env.ErrOther, "embedded compat.json", err)
	}
	lookupLatest(ctx, e, o)
	return nil
}

// lookupLatest replaces the embedded answer with a published one, when asked.
//
// The embedded version is refreshed weekly in the repository, which keeps it
// right for anyone building from source and wrong for anyone running a binary
// they downloaded months ago. Asking GitHub fixes that, and stays off by
// default: a diagnostic tool that reaches the network without being told to is
// not one that can be run on a locked-down machine.
func lookupLatest(ctx context.Context, e *env.Env, o Options) {
	if !o.Online {
		return
	}
	info, err := release.Lookup(ctx, nil, release.DefaultCachePath(), time.Now())
	if info.Stable == "" {
		// Leave the embedded answer in place. A version number nobody could
		// look up is not worth failing a whole collector over, but it is
		// worth saying, because the report will otherwise look as though
		// --online had worked.
		if err != nil {
			e.Runtime.LatestStableSource += " (--online failed: " + err.Error() + ")"
		}
		return
	}
	e.Runtime.LatestStable = env.Ok(info.Stable, info.Source())
	e.Runtime.LatestStableSource = info.Source()
	e.Runtime.LatestPrerelease = info.Prerelease
	if err != nil {
		// A stale cache used because the fetch failed: the answer is still
		// better than the embedded one, and the reader should know its age.
		e.Runtime.LatestStableSource += " (cached; refresh failed: " + err.Error() + ")"
	}
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

	// Asked once for the whole listing rather than once per distribution.
	// This does not start the utility VM; see running_windows.go.
	running, runErr := runningDistros(ctx, o.Timeout)
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
		const runSrc = "wsl --list --running --quiet"
		if runErr != nil {
			d.Running = env.Fail[bool](kindOf(runErr), runSrc, runErr)
		} else {
			d.Running = env.Ok(running[d.Name], runSrc)
		}
		if d.Version == 2 && d.BasePath != "" {
			vhdName := d.VhdFileName
			if vhdName == "" {
				vhdName = "ext4.vhdx"
			}
			base := strings.TrimPrefix(d.BasePath, `\\?\`)
			d.Vhd, d.Ext4 = inspectVhd(filepath.Join(base, vhdName))
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
	// Read after the loop, so every distribution already knows whether it is
	// running: that is what decides whether the file can be read at all.
	readWslConfFor(ctx, out, o.Timeout)
	// Both read inside the running distributions and both are bounded by what
	// is left of this collector's deadline, so they run together rather than
	// one of them spending the budget the other needs.
	var inside sync.WaitGroup
	inside.Add(2)
	go func() { defer inside.Done(); scanZoneFilesFor(ctx, out, o.Timeout) }()
	go func() { defer inside.Done(); readWatchersFor(ctx, out, o.Timeout) }()
	inside.Wait()
	e.Distros = env.Ok(out, src)
	return nil
}

// classifyOwner compares a file owner SID with the current process user.
func classifyOwner(sid string) string {
	switch sid {
	case "S-1-5-32-544":
		return env.OwnerAdministrators
	case "S-1-5-18":
		return env.OwnerSystem
	}
	if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil && tu.User.Sid != nil {
		if strings.EqualFold(tu.User.Sid.String(), sid) {
			return env.OwnerCurrentUser
		}
	}
	return env.OwnerOther
}

func intValue(k registry.Key, name string) int {
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return -1
	}
	return int(v)
}

// inspectVhd reads the file's shape and, from inside it, the guest
// filesystem's own account of itself. Both come from one shared-read handle:
// the VHDX of a running distribution is open in the VM, and a second handle
// that asks for exclusive access would simply fail.
func inspectVhd(path string) (env.Field[env.VhdInfo], env.Field[ext4.Super]) {
	info := env.VhdInfo{Path: path}
	var fs env.Field[ext4.Super]
	fi, err := os.Stat(path)
	if err != nil {
		return env.Fail[env.VhdInfo](kindOf(err), path, err), fs
	}
	info.FileSize = uint64(fi.Size())
	if a, err := fileinfo.Attributes(path); err == nil {
		info.Sparse, info.Compressed, info.Encrypted = a.Sparse, a.Compressed, a.Encrypted
	}
	if sid, err := fileinfo.OwnerSID(path); err == nil {
		info.OwnerSID = sid
		info.Owner = classifyOwner(sid)
	}
	h, err := fileinfo.OpenShared(path)
	if err != nil {
		info.ParseErr = "open: " + err.Error()
		if kindOf(err) == env.ErrNeedsElevation {
			return env.Fail[env.VhdInfo](env.ErrNeedsElevation, path, err), fs
		}
		return env.Ok(info, path), fs // stat worked; parse did not
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	disk, perr := vhdx.Open(f, fi.Size())
	parsed := disk.Info()
	info.MagicOK = parsed.MagicOK
	info.HeaderOK = parsed.HeaderOK
	info.VirtualSize = parsed.VirtualSize
	info.BlockSize = parsed.BlockSize
	info.AllocatedBytes = parsed.AllocatedBytes
	if perr != nil {
		info.ParseErr = perr.Error()
		return env.Ok(info, path), fs
	}
	fs = readSuperblock(disk, path)
	return env.Ok(info, path), fs
}

// readSuperblock reads the ext4 superblock out of the virtual disk. WSL formats
// the whole disk, with no partition table, so the filesystem starts at virtual
// offset 0 and its superblock 1024 bytes in.
func readSuperblock(disk *vhdx.Reader, path string) env.Field[ext4.Super] {
	src := path + " (ext4 superblock)"
	buf := make([]byte, ext4.SuperSize)
	if _, err := disk.ReadAt(buf, ext4.SuperOffset); err != nil {
		return env.Fail[ext4.Super](env.ErrOther, src, err)
	}
	s, err := ext4.ParseSuper(buf)
	if err != nil {
		kind := env.ErrOther
		if errors.Is(err, ext4.ErrNotExt4) {
			// Not ext4 at all: a disk this tool has no business
			// judging, not a disk in trouble.
			kind = env.ErrNotPresent
		}
		return env.Fail[ext4.Super](kind, src, err)
	}
	return env.Ok(s, src)
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
		// Resolve every path-typed value so the (pure) linter can report missing files.
		if table, terr := data.LoadConfigKeys(); terr == nil {
			cfg := wslconfig.Parse(string(b))
			for _, ent := range cfg.Entries {
				k, ok := table.Lookup(ent.Section, ent.Key)
				if !ok || k.Type != "path" || ent.Value == "" {
					continue
				}
				real := strings.ReplaceAll(ent.Value, `\\`, `\`)
				if e.Config.PathsExist == nil {
					e.Config.PathsExist = map[string]bool{}
				}
				_, serr := os.Stat(real)
				e.Config.PathsExist[strings.ToLower(real)] = serr == nil
			}
		}
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

	// The channels that say why a VM refused to start, and why its network
	// could not be configured, are the two most useful logs there are for
	// this tool and both are readable only by an administrator (measured:
	// all four answer "Access is denied" unelevated, ADR 0007). They are
	// still attempted, because the refusal is itself worth recording: it is
	// what lets a check say that --elevated would have more to go on rather
	// than reporting a quiet machine.
	for _, ch := range []string{
		"Microsoft-Windows-Hyper-V-Compute-Admin",
		"Microsoft-Windows-Hyper-V-Compute-Operational",
		"Microsoft-Windows-Host-Network-Service-Admin",
		"Microsoft-Windows-Host-Network-Service-Operational",
	} {
		if f, ok := e.Events.Channels[ch]; ok && f.ErrKind == env.ErrNeedsElevation {
			// Already known to be out of reach; asking again only costs
			// time in a collector that has a deadline.
			continue
		}
		evs, err = evtlog.Query(ch, `*[System[Level<=2 and `+since+`]]`, 40)
		if err != nil {
			if _, seen := e.Events.Channels[ch]; !seen {
				e.Events.Channels[ch] = env.Fail[env.ChannelInfo](kindOf(err), ch, err)
			}
			continue
		}
		add(ch, evs, nil)
	}

	if firstErr != nil && len(recent) == 0 {
		e.Events.Recent = env.Fail[[]env.Event](kindOf(firstErr), "wevtapi", firstErr)
		return firstErr
	}
	if recent == nil {
		recent = []env.Event{}
	}
	e.Events.Recent = env.Ok(recent, "wevtapi Application/System/VmSwitch/HCS/HNS")
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
	seenPath := map[string]bool{}
	for _, n := range names {
		pl := env.Plugin{Name: n}
		_, valType, err := k.GetValue(n, nil)
		if err != nil {
			continue
		}
		pl.ValueType = regTypeName(valType)
		if valType != registry.SZ && valType != registry.EXPAND_SZ {
			out = append(out, pl) // WSL skips it: "Plugin value has incorrect type"
			continue
		}
		p, _, err := k.GetStringValue(n)
		if err != nil {
			continue
		}
		if valType == registry.EXPAND_SZ {
			if ex, err := registry.ExpandString(p); err == nil {
				p = ex
			}
		}
		pl.Path = p
		key := strings.ToLower(p)
		if seenPath[key] {
			pl.Duplicate = true
		}
		seenPath[key] = true
		if _, err := os.Stat(p); err == nil {
			pl.Exists = true
			pl.Version, _ = fileinfo.Version(p)
			pl.Signature, _ = authenticode.Verify(p)
			has, perr := peexport.Has(p, "WSLPluginAPI_EntryPointV1")
			pl.EntryPoint = has
			if perr != nil {
				pl.ExportsErr = perr.Error()
			}
		}
		out = append(out, pl)
	}
	e.Plugins = env.Ok(out, src)
	return nil
}

func regTypeName(t uint32) string {
	switch t {
	case registry.SZ:
		return "REG_SZ"
	case registry.EXPAND_SZ:
		return "REG_EXPAND_SZ"
	case registry.MULTI_SZ:
		return "REG_MULTI_SZ"
	case registry.DWORD:
		return "REG_DWORD"
	case registry.QWORD:
		return "REG_QWORD"
	case registry.BINARY:
		return "REG_BINARY"
	}
	return fmt.Sprintf("type %d", t)
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

// ---------------------------------------------------------------- network adapters

func collectAdapters(ctx context.Context, e *env.Env, o Options) error {
	const src = "GetAdaptersAddresses"
	as, err := netinfo.Adapters()
	if err != nil {
		e.Net.Adapters = env.Fail[[]env.Adapter](kindOf(err), src, err)
		return err
	}
	out := make([]env.Adapter, 0, len(as))
	for _, a := range as {
		out = append(out, env.Adapter{
			Name: a.Name, Description: a.Description, IfType: a.IfType, Up: a.Up, VPN: a.VPN,
			Loopback: a.Loopback, Tunnel: a.Tunnel, DNSServers: a.DNSServers, DNSSuffix: a.DNSSuffix,
			HasIPv4: a.HasIPv4, HasIPv6: a.HasIPv6,
		})
	}
	e.Net.Adapters = env.Ok(out, src)
	return nil
}

// ---------------------------------------------------------------- Hyper-V firewall support

// collectFirewall mirrors GetHyperVFirewallSupportVersion in WslCoreFirewallSupport.cpp.
func collectFirewall(ctx context.Context, e *env.Env, o Options) error {
	const src = `MpsSvc\Parameters\HyperVFirewallDisable + ROOT\standardcimv2 MSFT_NetFirewallHyperV*`
	var fw env.FirewallSupport
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\MpsSvc\Parameters`, registry.READ); err == nil {
		if v, _, err := k.GetIntegerValue("HyperVFirewallDisable"); err == nil && v == 1 {
			fw.DisabledByRegistry = true
		}
		k.Close()
	}
	// "Invalid class" is the expected answer on hosts without Hyper-V firewall (Windows 10).
	if rows, err := wmi.Query(ctx, `ROOT\standardcimv2`, "SELECT VMCreatorId FROM MSFT_NetFirewallHyperVVMCreator", "VMCreatorId"); err == nil {
		fw.V1 = true
		_ = rows
	} else if errors.Is(err, context.DeadlineExceeded) {
		e.Net.HyperVFirewall = env.Fail[env.FirewallSupport](env.ErrTimeout, src, err)
		return err
	}
	if fw.V1 {
		if rows, err := wmi.Query(ctx, `ROOT\standardcimv2`, "SELECT Name FROM MSFT_NetFirewallHyperVProfile", "Name"); err == nil && len(rows) > 0 {
			fw.V2 = true
		}
	}
	e.Net.HyperVFirewall = env.Ok(fw, src)
	return nil
}

// ---------------------------------------------------------------- hotfixes

// trackedHotfixes are the updates with known WSL regressions.
var trackedHotfixes = []string{"KB5068861"} // mirrored networking + VPN, microsoft/WSL#13724

// collectHotfixes queries Win32_QuickFixEngineering only when mirrored mode is
// configured, because the query costs about a second.
func collectHotfixes(ctx context.Context, e *env.Env, o Options) error {
	const src = "Win32_QuickFixEngineering"
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".wslconfig"))
	if err != nil || !strings.Contains(strings.ToLower(string(b)), "mirrored") {
		e.Net.Hotfixes = env.Absent[map[string]bool](src + " (not queried: mirrored mode not configured)")
		return nil
	}
	var clauses []string
	for _, kb := range trackedHotfixes {
		clauses = append(clauses, "HotFixID='"+kb+"'")
	}
	rows, err := wmi.Query(ctx, `root\cimv2`, "SELECT HotFixID FROM Win32_QuickFixEngineering WHERE "+strings.Join(clauses, " OR "), "HotFixID")
	if err != nil {
		e.Net.Hotfixes = env.Fail[map[string]bool](kindOf(err), src, err)
		return err
	}
	m := map[string]bool{}
	for _, kb := range trackedHotfixes {
		m[kb] = false
	}
	for _, r := range rows {
		m[strings.ToUpper(fmt.Sprint(r["HotFixID"]))] = true
	}
	e.Net.Hotfixes = env.Ok(m, src)
	return nil
}

// ---------------------------------------------------------------- WSL policies

func collectPolicy(ctx context.Context, e *env.Env, o Options) error {
	const src = `HKLM\Software\Policies\WSL`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\Policies\WSL`, registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			e.Net.Policy = env.Absent[map[string]string](src)
			return nil
		}
		e.Net.Policy = env.Fail[map[string]string](kindOf(err), src, err)
		return err
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil {
		e.Net.Policy = env.Fail[map[string]string](kindOf(err), src, err)
		return err
	}
	m := map[string]string{}
	for _, n := range names {
		if s, _, err := k.GetStringValue(n); err == nil {
			m[n] = s
		} else if v, _, err := k.GetIntegerValue(n); err == nil {
			m[n] = strconv.FormatUint(v, 10)
		}
	}
	e.Net.Policy = env.Ok(m, src)
	return nil
}

// ---------------------------------------------------------------- wslkit guest agent

func collectAgent(ctx context.Context, e *env.Env, o Options) error {
	src := agenthost.StatusPath()
	st, ok, err := agenthost.ReadStatus()
	if err != nil {
		e.Agent = env.Fail[env.AgentInfo](kindOf(err), src, err)
		return err
	}
	if !ok {
		e.Agent = env.Absent[env.AgentInfo](src)
		return nil
	}
	info := env.AgentInfo{PID: st.PID, Version: st.Version, VMID: st.VMID, UpdatedAt: st.UpdatedAt, LastError: st.LastError, Guests: []string{}}
	if p, err := os.FindProcess(st.PID); err == nil {
		_ = p.Release()
		info.Alive = true
	}
	for _, s := range st.Sessions {
		info.Guests = append(info.Guests, s.Distro)
	}
	_, info.Autostart = agentinstall.Autostart()
	if cfg, err := agentconfig.LoadHost(filepath.Join(agentconfig.HostDir(), "host.json")); err == nil {
		info.AllowedNum = len(cfg.Allow)
	}
	e.Agent = env.Ok(info, src)
	return nil
}

var _ = time.Second
