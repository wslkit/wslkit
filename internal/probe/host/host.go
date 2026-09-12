// Package host holds Windows host probes (HST***, DEF***, PLG***).
package host

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
	"github.com/wslkit/wsldoctor/internal/wslver"
)

func All() []probe.Probe {
	return []probe.Probe{Features{}, Services{}, Virtualization{}, Build{}, Defender{}, Plugins{}}
}

// ---------------------------------------------------------------- HST001

type Features struct{}

func (Features) base() probe.Base {
	return probe.Base{PID: "HST001", PTitle: "Windows optional features", PMilestone: "M1"}
}
func (p Features) ID() string        { return p.base().ID() }
func (p Features) Title() string     { return p.base().Title() }
func (p Features) Milestone() string { return p.base().Milestone() }
func (p Features) Needs() []string   { return nil }

func (p Features) Run(e *env.Env) probe.Result {
	b := p.base()
	f := e.Host.Features
	if !f.OK() {
		if f.NeedsElevation() {
			return b.NeedsElevation("optional feature state")
		}
		return b.Res(probe.Unknown, 0.1, "could not read optional features via WMI: "+f.Err)
	}
	vmp := e.Host.Feature("VirtualMachinePlatform")
	wslf := e.Host.Feature("Microsoft-Windows-Subsystem-Linux")
	hv := e.Host.Feature("Microsoft-Hyper-V-All")

	describe := func(n int) string {
		switch n {
		case env.FeatureEnabled:
			return "enabled"
		case env.FeatureDisabled:
			return "disabled"
		case env.FeatureAbsent:
			return "absent"
		default:
			return "not returned by WMI"
		}
	}
	detail := fmt.Sprintf("VirtualMachinePlatform: %s\nMicrosoft-Windows-Subsystem-Linux: %s\nMicrosoft-Hyper-V-All: %s (optional)", describe(vmp), describe(wslf), describe(hv))

	if vmp == env.FeatureUnknown {
		// Seen on cold runners: Win32_OptionalFeature answers with a partial list.
		r := b.Res(probe.Unknown, 0.2, "WMI did not report the VirtualMachinePlatform feature; state unknown")
		r.Detail = detail
		r.FixHint = "re-run wsldoctor check; or: dism.exe /online /get-featureinfo /featurename:VirtualMachinePlatform   (admin)"
		return r
	}
	if vmp != env.FeatureEnabled {
		r := b.Res(probe.Fail, 0.9, "VirtualMachinePlatform is not enabled; WSL 2 cannot start a VM")
		r.Detail = detail + "\nThis presents as: Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED or error 0x80370102."
		r.FixHint = "dism.exe /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart   (admin, then reboot)"
		r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/troubleshooting"}
		return r
	}
	if wslf != env.FeatureEnabled && !e.Runtime.Version.OK() {
		r := b.Res(probe.Warn, 0.6, "Microsoft-Windows-Subsystem-Linux feature is not enabled and no Store runtime found")
		r.Detail = detail
		r.FixHint = "wsl --install --no-distribution"
		return r
	}
	r := b.Res(probe.OK, 0.7, "VirtualMachinePlatform enabled")
	r.Detail = detail
	return r
}

// ---------------------------------------------------------------- HST002

type Services struct{}

func (Services) base() probe.Base {
	return probe.Base{PID: "HST002", PTitle: "WSL and Hyper-V services", PMilestone: "M1"}
}
func (p Services) ID() string        { return p.base().ID() }
func (p Services) Title() string     { return p.base().Title() }
func (p Services) Milestone() string { return p.base().Milestone() }
func (p Services) Needs() []string   { return nil }

func (p Services) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Host.Services.OK() {
		return b.Res(probe.Unknown, 0.1, "could not query services: "+e.Host.Services.Err)
	}
	type req struct {
		name, why string
		required  bool
	}
	// LxssManager is informational: it exists only with the legacy in-box WSL,
	// and its absence is WSL002's finding ("not installed"), never a second FAIL here.
	reqs := []req{
		{"WSLService", "Store/MSI WSL runtime service", e.Runtime.Version.OK()},
		{"LxssManager", "in-box WSL service (legacy)", false},
		{"vmcompute", "Hyper-V Host Compute Service; creates the WSL VM", true},
		{"HvHost", "Hyper-V host service", true},
	}
	var lines, fails []string
	for _, rq := range reqs {
		s, ok := e.Service(rq.name)
		if !ok || !s.Exists {
			if rq.required {
				fails = append(fails, fmt.Sprintf("%s is missing (%s)", rq.name, rq.why))
			}
			lines = append(lines, fmt.Sprintf("%-12s missing", rq.name))
			continue
		}
		lines = append(lines, fmt.Sprintf("%-12s %-8s start=%s", rq.name, s.State, s.StartType))
		if rq.required && strings.EqualFold(s.StartType, "Disabled") {
			fails = append(fails, fmt.Sprintf("%s is Disabled (%s)", rq.name, rq.why))
		}
	}
	sort.Strings(lines)
	if len(fails) > 0 {
		r := b.Res(probe.Fail, 0.85, fails[0])
		r.Detail = strings.Join(lines, "\n") + "\n\n" + strings.Join(fails, "\n")
		r.FixHint = "sc.exe config <service> start= demand   (admin); if missing, repair with: wsl --install --no-distribution"
		return r
	}
	r := b.Res(probe.OK, 0.6, "WSL and Hyper-V services present and not disabled")
	r.Detail = strings.Join(lines, "\n")
	return r
}

// ---------------------------------------------------------------- HST003

type Virtualization struct{}

func (Virtualization) base() probe.Base {
	return probe.Base{PID: "HST003", PTitle: "Hardware virtualization / hypervisor", PMilestone: "M1"}
}
func (p Virtualization) ID() string        { return p.base().ID() }
func (p Virtualization) Title() string     { return p.base().Title() }
func (p Virtualization) Milestone() string { return p.base().Milestone() }
func (p Virtualization) Needs() []string   { return nil }

func (p Virtualization) Run(e *env.Env) probe.Result {
	b := p.base()
	hv := e.Host.HypervisorPresent
	if !hv.OK() {
		return b.Res(probe.Unknown, 0.1, "could not determine hypervisor state: "+hv.Err)
	}
	if hv.Value {
		return b.Res(probe.OK, 0.7, "Windows hypervisor is running (HypervisorPresent=true)")
	}
	r := b.Res(probe.Fail, 0.9, "No hypervisor is running; WSL 2 cannot start")
	r.Detail = "Win32_ComputerSystem.HypervisorPresent is false. Either virtualization (VT-x / AMD-V / SVM) is disabled in firmware, " +
		"or the Windows hypervisor launch type is off, or a competing hypervisor (VMware, VirtualBox in non-Hyper-V mode) owns the CPU.\n" +
		"This presents as: error 0x80370102 'The virtual machine could not be started because a required feature is not installed'."
	if s, ok := e.Service("vmx86"); ok && s.Exists {
		r.Detail += "\nVMware driver service (vmx86) is present."
	}
	if s, ok := e.Service("VBoxSup"); ok && s.Exists {
		r.Detail += "\nVirtualBox driver service (VBoxSup) is present."
	}
	r.FixHint = "bcdedit /set hypervisorlaunchtype auto   (admin, reboot) and enable virtualization in firmware"
	r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/troubleshooting#error-0x80370102-the-virtual-machine-could-not-be-started-because-a-required-feature-is-not-installed"}
	return r
}

// ---------------------------------------------------------------- DEF001

type Defender struct{}

func (Defender) base() probe.Base {
	return probe.Base{PID: "DEF001", PTitle: "Windows Defender exclusions for WSL", PMilestone: "M1"}
}
func (p Defender) ID() string        { return p.base().ID() }
func (p Defender) Title() string     { return p.base().Title() }
func (p Defender) Milestone() string { return p.base().Milestone() }
func (p Defender) Needs() []string   { return nil }

func (p Defender) Run(e *env.Env) probe.Result {
	b := p.base()
	d := e.Defender
	if d.Present.Absent() || (d.Present.OK() && !d.Present.Value) {
		return b.Res(probe.Skipped, 0, "skipped: Microsoft Defender is not the active antivirus")
	}
	if d.RealtimeEnabled.OK() && !d.RealtimeEnabled.Value {
		return b.Res(probe.OK, 0.5, "Defender real-time protection is off; exclusions are moot")
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.Skipped, 0, "skipped: no distributions to protect")
	}
	if d.Exclusions.NeedsElevation() {
		r := b.NeedsElevation("Defender exclusion list")
		r.Summary = "Defender real-time protection is on; exclusions cannot be verified without elevation"
		r.Detail = fmt.Sprintf("%d distro VHDX file(s) may be scanned on every write. Reading the exclusion list needs admin on this Windows build.", len(distros))
		r.FixID = "defender"
		return r
	}
	if !d.Exclusions.OK() {
		return b.Res(probe.Unknown, 0.1, "could not read Defender exclusions: "+d.Exclusions.Err)
	}
	ex := d.Exclusions.Value
	var missing []string
	for _, dist := range distros {
		if !dist.Vhd.OK() {
			continue
		}
		if !pathExcluded(dist.Vhd.Value.Path, ex.Paths) {
			missing = append(missing, dist.Vhd.Value.Path)
		}
	}
	var missingProc []string
	for _, proc := range []string{"vmmem", "vmmemWSL", "wslservice.exe", "wsl.exe"} {
		if !containsFold(ex.Processes, proc) {
			missingProc = append(missingProc, proc)
		}
	}
	if len(missing) == 0 {
		r := b.Res(probe.OK, 0.6, "All distro disks are excluded from Defender real-time scanning")
		if len(missingProc) > 0 {
			r.Detail = "Process exclusions not set for: " + strings.Join(missingProc, ", ") + " (optional)"
		}
		return r
	}
	r := b.Res(probe.Warn, 0.55, fmt.Sprintf("Defender scans %d WSL disk(s) on every write", len(missing)))
	r.Detail = "No exclusion for:\n  " + strings.Join(missing, "\n  ")
	if len(missingProc) > 0 {
		r.Detail += "\nProcess exclusions not set for: " + strings.Join(missingProc, ", ")
	}
	r.FixID = "defender"
	r.FixHint = "wsldoctor fix defender     (requires admin)"
	r.Refs = []string{"https://github.com/microsoft/WSL/issues/8995"}
	return r
}

func pathExcluded(path string, exclusions []string) bool {
	p := strings.ToLower(strings.TrimPrefix(path, `\\?\`))
	for _, ex := range exclusions {
		x := strings.ToLower(strings.TrimSuffix(ex, `\`))
		if x == "" {
			continue
		}
		if p == x || strings.HasPrefix(p, x+`\`) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- PLG001

type Plugins struct{}

func (Plugins) base() probe.Base {
	return probe.Base{PID: "PLG001", PTitle: "WSL host plugins", PMilestone: "M1"}
}
func (p Plugins) ID() string        { return p.base().ID() }
func (p Plugins) Title() string     { return p.base().Title() }
func (p Plugins) Milestone() string { return p.base().Milestone() }
func (p Plugins) Needs() []string   { return nil }

func (p Plugins) Run(e *env.Env) probe.Result {
	b := p.base()
	if e.Plugins.Absent() {
		return b.Res(probe.OK, 0.5, "No WSL host plugins registered")
	}
	if !e.Plugins.OK() {
		return b.Res(probe.Unknown, 0.1, "could not read plugin registry: "+e.Plugins.Err)
	}
	plugins := e.Plugins.Value
	if len(plugins) == 0 {
		return b.Res(probe.OK, 0.5, "No WSL host plugins registered")
	}
	var lines, fatal, warns []string
	for _, pl := range plugins {
		problem := pluginProblem(pl)
		state := "ok"
		if problem != "" {
			state = problem
		}
		lines = append(lines, fmt.Sprintf("%-28s %-10s %s  %s", pl.Name, pl.Version, pl.Path, state))
		switch {
		case problem == "":
		case pl.ValueType != "" && pl.ValueType != "REG_SZ" && pl.ValueType != "REG_EXPAND_SZ", pl.Duplicate:
			// WSL logs and skips these; the plugin simply does not load.
			warns = append(warns, fmt.Sprintf("%s: %s (WSL skips it; the owning product will not work)", pl.Name, problem))
		default:
			fatal = append(fatal, fmt.Sprintf("%s: %s", pl.Name, problem))
		}
		if knownBad := knownBadPlugin(e, pl); knownBad != "" {
			warns = append(warns, knownBad)
		}
	}
	if len(fatal) > 0 {
		r := b.Res(probe.Fail, 0.85, fatal[0])
		r.Detail = strings.Join(lines, "\n") + "\n\n" + strings.Join(append(fatal, warns...), "\n") +
			"\nWSL reports a failed plugin as: \"A fatal error was returned by plugin '<name>'\" (or, for an outdated API, \"The plugin '<name>' requires a newer version of WSL. Please run: wsl.exe --update\"). wslservice does not start until the plugin loads."
		r.FixHint = "reinstall or update the product that registered the plugin (Docker Desktop, Defender for Endpoint, ...), or remove its value under HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Lxss\\Plugins (admin)"
		r.Refs = []string{"https://github.com/microsoft/WSL/blob/master/src/windows/service/exe/PluginManager.cpp"}
		return r
	}
	if len(warns) > 0 {
		r := b.Res(probe.Warn, 0.5, warns[0])
		r.Detail = strings.Join(lines, "\n") + "\n\n" + strings.Join(warns, "\n")
		r.FixHint = "reinstall the owning product, or clean the value under HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Lxss\\Plugins (admin)"
		return r
	}
	r := b.Res(probe.OK, 0.5, fmt.Sprintf("%d WSL host plugin(s) registered, all present, signed and exporting the entry point", len(plugins)))
	r.Detail = strings.Join(lines, "\n")
	return r
}

// pluginProblem applies PluginManager::LoadPlugins' checks in its order.
func pluginProblem(pl env.Plugin) string {
	switch {
	case pl.ValueType != "" && pl.ValueType != "REG_SZ" && pl.ValueType != "REG_EXPAND_SZ":
		return "registry value is " + pl.ValueType + ", not REG_SZ"
	case pl.Duplicate:
		return "same DLL as an earlier plugin value; duplicate skipped"
	case !pl.Exists:
		return "DLL missing: " + pl.Path
	case pl.Signature == "unsigned":
		return "DLL is not Authenticode-signed; official WSL builds refuse to load it (TRUST_E_NOSIGNATURE)"
	case pl.Signature == "bad_digest":
		return "DLL signature does not match the file (TRUST_E_BAD_DIGEST); the file was modified or corrupted"
	case pl.Signature == "untrusted_root", pl.Signature == "distrusted":
		return "DLL signature chains to an untrusted root (" + pl.Signature + ")"
	case pl.ExportsErr != "":
		return "cannot read the DLL's export table: " + pl.ExportsErr
	case pl.Exists && pl.Signature != "" && !pl.EntryPoint:
		return "DLL does not export WSLPluginAPI_EntryPointV1; it is not a WSL plugin"
	}
	return ""
}

// knownBadPlugin flags plugin/runtime pairs with a known startup failure.
func knownBadPlugin(e *env.Env, pl env.Plugin) string {
	if !e.Runtime.Version.OK() {
		return ""
	}
	lower := strings.ToLower(pl.Name + " " + pl.Path)
	if strings.Contains(lower, "mde") || strings.Contains(lower, "defender") || strings.Contains(lower, "sense") {
		if rt, err := wslver.Parse(e.Runtime.Version.Value); err == nil && rt.Less(wslver.MustParse("2.7.13")) {
			return fmt.Sprintf("%s looks like the Defender for Endpoint plugin; WSL %s had a startup failure with it that 2.7.13 fixed (wsl --update)", pl.Name, rt)
		}
	}
	return ""
}
