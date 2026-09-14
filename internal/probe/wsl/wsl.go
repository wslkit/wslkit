// Package wsl holds runtime and distro probes (WSL001–WSL007, ZON001, MNT001).
package wsl

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslver"
)

// All returns the probes in this package in execution order.
func All() []probe.Probe {
	return []probe.Probe{Installed{}, Compat{}, UpdateAvailable{}, Inventory{}, ConfigLint{}, WslConf{}, ZoneFiles{}, Watchers{}}
}

// ---------------------------------------------------------------- WSL002

type Installed struct{}

func (Installed) base() probe.Base {
	return probe.Base{PID: "WSL002", PTitle: "WSL runtime installed", PMilestone: "M1"}
}
func (p Installed) ID() string        { return p.base().ID() }
func (p Installed) Title() string     { return p.base().Title() }
func (p Installed) Milestone() string { return p.base().Milestone() }
func (p Installed) Needs() []string   { return nil }

func (p Installed) Run(e *env.Env) probe.Result {
	b := p.base()
	rt := e.Runtime
	if e.Net.Policy.OK() {
		if v, ok := e.Net.Policy.Value["AllowWSL"]; ok && v == "0" {
			r := b.Res(probe.Fail, 0.95, "WSL is disabled by group policy (AllowWSL=0)")
			r.Detail = "HKLM\\Software\\Policies\\WSL\\AllowWSL is 0. wsl.exe refuses to run regardless of what is installed. This is set by your organisation (Intune / GPO), not by a bug."
			r.FixHint = "ask your IT administrator to allow WSL (policy AllowWSL); nothing on this machine can override it"
			r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/enterprise"}
			return r
		}
	}
	switch {
	case rt.Version.OK():
		r := b.Res(probe.OK, 1, fmt.Sprintf("WSL %s installed", trimVer(rt.Version.Value)))
		var parts []string
		if rt.InstallLocation.OK() {
			parts = append(parts, "location "+rt.InstallLocation.Value)
		}
		if rt.AppxFullName.OK() {
			parts = append(parts, "package "+rt.AppxFullName.Value)
		} else if rt.MSIVersion.OK() {
			parts = append(parts, "MSI "+rt.MSIVersion.Value)
		}
		r.Detail = strings.Join(parts, "\n")
		return r
	case rt.Version.NeedsElevation():
		return b.NeedsElevation("WSL runtime version")
	case rt.InboxWslVersion.OK() && legacyInboxPresent(e):
		// The LxssManager service only exists with the legacy in-box WSL. The
		// optional-feature state is not a usable signal: Windows 11 24H2+ reports
		// Microsoft-Windows-Subsystem-Linux as enabled on machines with no WSL at all.
		r := b.Res(probe.Fail, 0.85, "Only the in-box (legacy) WSL is present; the Store/MSI runtime is not installed")
		r.Detail = fmt.Sprintf("C:\\Windows\\System32\\wsl.exe is version %s (an OS build number, not a WSL release) and the\n"+
			"legacy LxssManager service is registered. Modern WSL 2, systemd, mirrored networking and every fix\n"+
			"in this tool need the Store runtime.", rt.InboxWslVersion.Value)
		r.FixID = "update"
		r.FixHint = "wsl --install --no-distribution     (or: wslkit doctor fix update)"
		r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/install"}
		return r
	case rt.InboxWslVersion.OK():
		r := b.Res(probe.Fail, 0.9, "WSL is not installed; only the in-box installer stub wsl.exe is present")
		r.Detail = fmt.Sprintf("C:\\Windows\\System32\\wsl.exe %s is the stub Windows ships to bootstrap WSL from the Store. No runtime is installed.", rt.InboxWslVersion.Value)
		r.FixHint = "wsl --install --no-distribution     then     wsl --install -d Ubuntu"
		r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/install"}
		return r
	default:
		r := b.Res(probe.Fail, 0.9, "WSL is not installed")
		r.Detail = fieldErr("runtime version", rt.Version) + "\n" + fieldErr("inbox wsl.exe", rt.InboxWslVersion)
		r.FixHint = "wsl --install"
		r.Refs = []string{"https://learn.microsoft.com/en-us/windows/wsl/install"}
		return r
	}
}

// ---------------------------------------------------------------- WSL001

type Compat struct{}

func (Compat) base() probe.Base {
	return probe.Base{PID: "WSL001", PTitle: "Runtime version vs distro version compatibility", PMilestone: "M1", PNeeds: []string{"WSL002"}}
}
func (p Compat) ID() string        { return p.base().ID() }
func (p Compat) Title() string     { return p.base().Title() }
func (p Compat) Milestone() string { return p.base().Milestone() }
func (p Compat) Needs() []string   { return p.base().Needs() }

func (p Compat) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Runtime.Version.OK() {
		return b.Res(probe.Skipped, 0, "skipped: runtime version unknown")
	}
	rt, err := wslver.Parse(e.Runtime.Version.Value)
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "could not parse runtime version "+e.Runtime.Version.Value)
	}
	matrix, err := data.LoadCompat()
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "compat matrix failed to load: "+err.Error())
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.Skipped, 0, "skipped: no distributions registered")
	}
	modernMin := wslver.MustParse(matrix.ModernFormatMin)

	var worst probe.Result
	worst.Status = probe.OK
	var okNames []string
	for _, d := range distros {
		name := d.Name
		if d.Modern == 1 && rt.Less(modernMin) {
			r := b.Res(probe.Fail, 0.95, fmt.Sprintf("Runtime %s cannot load modern-format distro %s at all (needs >= %s)", rt, name, modernMin))
			r.Detail = fmt.Sprintf("%s is a modern/tar-format distro (Modern=1, %s %s). Modern-format support arrived in WSL %s.", name, d.Flavor, d.OsVersion, modernMin)
			r.FixID, r.FixHint = "update", "wslkit doctor fix update      (runs wsl --update)"
			r.Refs = []string{matrix.Refs["modern_format"]}
			return r
		}
		row := matrix.Lookup(d.Flavor, d.OsVersion)
		if row == nil {
			okNames = append(okNames, fmt.Sprintf("%s (%s %s, no matrix row)", name, d.Flavor, d.OsVersion))
			continue
		}
		r, sev := judge(b, matrix, rt, d, row)
		if sev > severity(worst) {
			worst = r
		}
		if sev == 0 {
			okNames = append(okNames, fmt.Sprintf("%s (%s %s)", name, d.Flavor, d.OsVersion))
		}
	}
	if worst.Status != probe.OK {
		return worst
	}
	r := b.Res(probe.OK, 0.6, fmt.Sprintf("Runtime %s is compatible with all %d registered distro(s)", rt, len(distros)))
	r.Detail = strings.Join(okNames, "\n")
	return r
}

func severity(r probe.Result) int {
	switch r.Status {
	case probe.Fail:
		return 2
	case probe.Warn:
		return 1
	}
	return 0
}

func judge(b probe.Base, matrix *data.Compat, rt wslver.Version, d env.Distro, row *data.DistroCompat) (probe.Result, int) {
	name := d.Name
	symptom := row.Symptom
	if symptom == "" {
		symptom = "launch failures"
	}
	if min, reqs := matrix.MinimumRuntime(row); !min.IsZero() {
		if rt.Less(min) {
			r := b.Res(probe.Fail, 0.9, fmt.Sprintf("Runtime %s is too old for %s (%s %s needs >= %s)", rt, name, row.Flavor, row.OsVersion, min))
			r.Detail = fmt.Sprintf("This will present as: %s", symptom)
			for _, req := range reqs {
				if rt.Less(req.Min) {
					r.Detail += fmt.Sprintf("\nNeeds: %s (runtime >= %s)", req.Title, req.Min)
					r.Refs = append(r.Refs, req.Refs...)
				}
			}
			if row.Cause != "" {
				r.Detail += "\nCause: " + row.Cause
			}
			r.FixID, r.FixHint = "update", "wslkit doctor fix update      (runs wsl --update)"
			r.Refs = append(r.Refs, row.Refs...)
			return r, 2
		}
		return b.Res(probe.OK, 0.8, ""), 0
	}
	if row.KnownBadMax != "" {
		bad := wslver.MustParse(row.KnownBadMax)
		if !bad.Less(rt) { // rt <= known bad
			r := b.Res(probe.Fail, 0.9, fmt.Sprintf("Runtime %s is known to fail with %s (%s %s)", rt, name, row.Flavor, row.OsVersion))
			r.Detail = fmt.Sprintf("Runtimes up to %s fail to boot this distro. This presents as: %s", bad, symptom)
			if row.KnownGoodMin != "" {
				r.Detail += fmt.Sprintf("\nRuntime %s and later are known to work.", row.KnownGoodMin)
			}
			r.FixID, r.FixHint, r.Refs = "update", "wslkit doctor fix update      (runs wsl --update)", row.Refs
			return r, 2
		}
	}
	if row.KnownGoodMin != "" {
		good := wslver.MustParse(row.KnownGoodMin)
		if rt.Less(good) {
			r := b.Res(probe.Warn, 0.5, fmt.Sprintf("Runtime %s is in an untested range for %s (%s %s)", rt, name, row.Flavor, row.OsVersion))
			r.Detail = fmt.Sprintf("Known to fail up to %s, known to work from %s. If launches fail with \"%s\", updating is the first thing to try.", row.KnownBadMax, good, symptom)
			r.FixID, r.FixHint, r.Refs = "update", "wslkit doctor fix update      (runs wsl --update)", row.Refs
			return r, 1
		}
	}
	return b.Res(probe.OK, 0.8, ""), 0
}

// ---------------------------------------------------------------- WSL003

type UpdateAvailable struct{}

func (UpdateAvailable) base() probe.Base {
	return probe.Base{PID: "WSL003", PTitle: "WSL update available", PMilestone: "M1", PNeeds: []string{"WSL002"}}
}
func (p UpdateAvailable) ID() string        { return p.base().ID() }
func (p UpdateAvailable) Title() string     { return p.base().Title() }
func (p UpdateAvailable) Milestone() string { return p.base().Milestone() }
func (p UpdateAvailable) Needs() []string   { return p.base().Needs() }

func (p UpdateAvailable) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Runtime.Version.OK() {
		return b.Res(probe.Skipped, 0, "skipped: runtime version unknown")
	}
	if !e.Runtime.LatestStable.OK() {
		return b.Res(probe.Unknown, 0.05, "latest stable version unknown (no embedded data and not --online)")
	}
	cur, err1 := wslver.Parse(e.Runtime.Version.Value)
	latest, err2 := wslver.Parse(e.Runtime.LatestStable.Value)
	if err1 != nil || err2 != nil {
		return b.Res(probe.Unknown, 0.05, "could not compare versions")
	}
	src := e.Runtime.LatestStableSource
	if src == "" {
		src = "embedded data"
	}
	// A pre-release is only worth mentioning when it is ahead of the stable
	// release, and only as a footnote: it is where a fix for a bug someone
	// is actually hitting turns up first, and it is not what to recommend to
	// someone whose machine is working.
	pre := ""
	if v := e.Runtime.LatestPrerelease; v != "" {
		if pv, err := wslver.Parse(v); err == nil && latest.Less(pv) {
			pre = fmt.Sprintf("\nPre-release %s is also published (wsl --update --pre-release). Worth trying only for a bug fixed in it.", pv)
		}
	}
	switch {
	case cur.Less(latest):
		r := b.Res(probe.Warn, 0.3, fmt.Sprintf("WSL %s installed; %s is the latest stable (%s)", cur, latest, src))
		r.Detail = "Not a root cause by itself, but most launch failures on old runtimes are fixed by updating." + pre
		r.FixID, r.FixHint = "update", "wslkit doctor fix update      (runs wsl --update)"
		return r
	case latest.Less(cur):
		r := b.Res(probe.OK, 0.5, fmt.Sprintf("WSL %s is newer than the latest stable this tool knows (%s); pre-release channel or newer data needed", cur, latest))
		r.Detail = strings.TrimPrefix(pre, "\n")
		return r
	default:
		r := b.Res(probe.OK, 0.7, fmt.Sprintf("WSL %s is the latest stable (%s)", cur, src))
		r.Detail = strings.TrimPrefix(pre, "\n")
		return r
	}
}

// ---------------------------------------------------------------- WSL004

type Inventory struct{}

func (Inventory) base() probe.Base {
	return probe.Base{PID: "WSL004", PTitle: "Distribution inventory and state", PMilestone: "M1", PNeeds: []string{"WSL002"}}
}
func (p Inventory) ID() string        { return p.base().ID() }
func (p Inventory) Title() string     { return p.base().Title() }
func (p Inventory) Milestone() string { return p.base().Milestone() }
func (p Inventory) Needs() []string   { return p.base().Needs() }

func (p Inventory) Run(e *env.Env) probe.Result {
	b := p.base()
	if e.Distros.NeedsElevation() {
		return b.NeedsElevation("distro registry")
	}
	if !e.Distros.OK() && !e.Distros.Absent() {
		return b.Res(probe.Unknown, 0.1, "could not read distro inventory: "+e.Distros.Err)
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		r := b.Res(probe.Warn, 0.4, "No distributions are registered for this user")
		r.Detail = "WSL is installed but there is nothing to run. If you expected a distro here, check you are logged in as the same Windows user who installed it."
		r.FixHint = "wsl --list --online    then    wsl --install -d <Name>"
		return r
	}
	var lines, warns []string
	for _, d := range distros {
		def := ""
		if d.IsDefault {
			def = " (default)"
		}
		modern := "appx"
		if d.Modern == 1 {
			modern = "modern"
		}
		lines = append(lines, fmt.Sprintf("%s%s  WSL%d  %s  %s %s  uid=%d  state=%d flags=%d", d.Name, def, d.Version, modern, d.Flavor, d.OsVersion, d.DefaultUid, d.State, d.Flags))
		switch {
		case d.State != 1:
			warns = append(warns, fmt.Sprintf("%s is in state %d (1 = normal; 3 = install in progress; 4 = uninstall in progress). An interrupted install or uninstall leaves this.", d.Name, d.State))
		case d.RunOOBE == 1 && d.DefaultUid == 0:
			warns = append(warns, fmt.Sprintf("%s: first-run setup never completed (RunOOBE=1, DefaultUid=0). Launch it once interactively to create the user.", d.Name))
		case d.Version == 1:
			warns = append(warns, fmt.Sprintf("%s runs as WSL 1. .wslconfig does not apply to it; convert with: wsl --set-version %s 2", d.Name, d.Name))
		}
	}
	if len(warns) > 0 {
		r := b.Res(probe.Warn, 0.45, fmt.Sprintf("%d distro(s) registered; %d need attention", len(distros), len(warns)))
		r.Detail = strings.Join(lines, "\n") + "\n\n" + strings.Join(warns, "\n")
		r.FixHint = "wsl --list --verbose"
		return r
	}
	r := b.Res(probe.OK, 0.5, fmt.Sprintf("%d distro(s) registered, all in normal state", len(distros)))
	r.Detail = strings.Join(lines, "\n")
	return r
}

// ---------------------------------------------------------------- helpers

// legacyInboxPresent is true when the pre-Store WSL component is registered.
func legacyInboxPresent(e *env.Env) bool {
	s, ok := e.Service("LxssManager")
	return ok && s.Exists
}

func trimVer(v string) string {
	if p, err := wslver.Parse(v); err == nil {
		return p.String()
	}
	return v
}

func fieldErr[T any](name string, f env.Field[T]) string {
	if f.OK() {
		return name + ": ok"
	}
	return fmt.Sprintf("%s: %s (%s)", name, f.ErrKind, f.Err)
}
