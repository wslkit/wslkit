package host

import (
	"fmt"
	"strings"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
)

// ---------------------------------------------------------------- HST004

// Build checks the Windows build against WSL 2 requirements and reports a
// pending reboot, which blocks feature changes and (since WSL 2.6.1) distro installs.
type Build struct{}

func (Build) base() probe.Base {
	return probe.Base{PID: "HST004", PTitle: "Windows build and pending reboot", PMilestone: "M2"}
}
func (p Build) ID() string        { return p.base().ID() }
func (p Build) Title() string     { return p.base().Title() }
func (p Build) Milestone() string { return p.base().Milestone() }
func (p Build) Needs() []string   { return nil }

const (
	minWSL2Build   = 19041 // Windows 10 2004
	win11Build     = 22000
	win11_22H2     = 22621
	win10EndOfLife = "2025-10-14"
)

func (p Build) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Host.OS.Collected() || !e.Host.OS.OK() || e.Host.OS.Value.Build == 0 {
		return b.Res(probe.Unknown, 0.1, "could not read the Windows version: "+e.Host.OS.Err)
	}
	os := e.Host.OS.Value
	var lines []string
	lines = append(lines, fmt.Sprintf("Windows %d.%d.%d.%d %s %s", os.Major, os.Minor, os.Build, os.UBR, os.DisplayVersion, os.EditionID))

	if os.Build < minWSL2Build {
		r := b.Res(probe.Fail, 0.9, fmt.Sprintf("Windows build %d is too old for WSL 2 (needs %d, Windows 10 version 2004)", os.Build, minWSL2Build))
		r.Detail = strings.Join(lines, "\n")
		r.FixHint = "install Windows updates (Settings > Windows Update); WSL 2 requires build 19041 or later"
		r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/install-manual"}
		return r
	}

	pending := e.Host.PendingReboot.OK() && e.Host.PendingReboot.Value
	if pending {
		lines = append(lines, "A reboot is pending (Component Based Servicing / Windows Update marker present).")
	}
	switch {
	case os.Build < win11Build:
		lines = append(lines, fmt.Sprintf("Windows 10 left mainstream support on %s. WSL 2.7.x still runs here, but mirrored networking, Hyper-V firewall and several [wsl2] keys need Windows 11.", win10EndOfLife))
	case os.Build < win11_22H2:
		lines = append(lines, "Windows 11 before 22H2: mirrored networking, dnsTunneling and firewall settings need build 22621 or later.")
	}

	if pending {
		noRuntime := !e.Runtime.Version.OK()
		noDistros := len(e.DistroList()) == 0
		conf := 0.45
		summary := "A reboot is pending; enabled features (VirtualMachinePlatform, Hyper-V) do not take effect until then"
		if noRuntime || noDistros {
			conf = 0.6
			summary = "A reboot is pending; WSL refuses to install distributions until you reboot (WSL 2.6.1+)"
		}
		r := b.Res(probe.Warn, conf, summary)
		r.Detail = strings.Join(lines, "\n")
		r.FixHint = "shutdown /r /t 0     (reboot, then re-run wsldoctor check)"
		r.Refs = []string{"https://github.com/microsoft/WSL/releases/tag/2.6.1"}
		return r
	}
	r := b.Res(probe.OK, 0.5, fmt.Sprintf("Windows build %d supports WSL 2; no reboot pending", os.Build))
	r.Detail = strings.Join(lines, "\n")
	return r
}

// ---------------------------------------------------------------- HST005

// COMClass checks that the Store/MSI service COM class is registered. A missing
// registration surfaces as REGDB_E_CLASSNOTREG (0x80040154), typically after a
// Windows feature update or a half-removed Store package.
type COMClass struct{}

func (COMClass) base() probe.Base {
	return probe.Base{PID: "HST005", PTitle: "WSL service COM registration", PMilestone: "M1", PNeeds: []string{"WSL002"}}
}
func (p COMClass) ID() string        { return p.base().ID() }
func (p COMClass) Title() string     { return p.base().Title() }
func (p COMClass) Milestone() string { return p.base().Milestone() }
func (p COMClass) Needs() []string   { return p.base().Needs() }

func (p COMClass) Run(e *env.Env) probe.Result {
	b := p.base()
	f := e.Runtime.COMClassRegistered
	if !f.Collected() {
		return b.Res(probe.Skipped, 0, "skipped: COM registration not collected (older snapshot)")
	}
	if !e.Runtime.Version.OK() {
		return b.Res(probe.Skipped, 0, "skipped: no Store/MSI runtime installed")
	}
	if !f.OK() {
		return b.Res(probe.Unknown, 0.1, "could not read the CLSID key: "+f.Err)
	}
	if f.Value {
		return b.Res(probe.OK, 0.6, "CLSID_LxssUserSession is registered for the installed runtime")
	}
	r := b.Res(probe.Fail, 0.8, fmt.Sprintf("WSL %s is installed but its COM class is not registered", e.Runtime.Version.Value))
	r.Detail = "HKCR\\CLSID\\{a9b7a1b9-0671-405c-95f1-e0612cb4ce7e} (CLSID_LxssUserSession) is missing. wsl.exe cannot reach wslservice and reports\n" +
		"Error code: Wsl/CallMsi/REGDB_E_CLASSNOTREG or 0x80040154. Typical after a Windows feature update or an interrupted Store update."
	r.FixID, r.FixHint = "update", "wsl --update      (re-registers the package; or repair Windows Subsystem for Linux in Settings > Apps)"
	r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/troubleshooting#error-0x80040154-after-windows-update"}
	return r
}
