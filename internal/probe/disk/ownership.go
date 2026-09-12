package disk

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslver"
)

// ---------------------------------------------------------------- DSK006

// Ownership flags VHDX files not owned by the current user. WSL 2.7.11/2.7.12
// fixed `wsl --move` / `--import` leaving disks owned by BUILTIN\Administrators,
// which made attaching them fail with ERROR_ACCESS_DENIED on older runtimes.
type Ownership struct{}

func (Ownership) base() probe.Base {
	return probe.Base{PID: "DSK006", PTitle: "VHDX file ownership", PMilestone: "M2", PNeeds: []string{"DSK002"}}
}
func (p Ownership) ID() string        { return p.base().ID() }
func (p Ownership) Title() string     { return p.base().Title() }
func (p Ownership) Milestone() string { return p.base().Milestone() }
func (p Ownership) Needs() []string   { return p.base().Needs() }

var ownershipFixed = wslver.MustParse("2.7.12")

func (p Ownership) Run(e *env.Env) probe.Result {
	b := p.base()
	var lines, foreign []string
	seen := 0
	for _, d := range e.DistroList() {
		v := d.Vhd
		if !v.OK() || v.Value.Owner == "" {
			continue
		}
		seen++
		lines = append(lines, fmt.Sprintf("%s: owner %s (%s)", d.Name, v.Value.Owner, v.Value.OwnerSID))
		if v.Value.Owner != env.OwnerCurrentUser {
			foreign = append(foreign, d.Name)
		}
	}
	if seen == 0 {
		return b.Res(probe.Skipped, 0, "skipped: VHDX ownership not collected")
	}
	if len(foreign) == 0 {
		r := b.Res(probe.OK, 0.4, "All distro VHDX files are owned by the current user")
		r.Detail = strings.Join(lines, "\n")
		return r
	}
	fixedRuntime := false
	if e.Runtime.Version.OK() {
		if rt, err := wslver.Parse(e.Runtime.Version.Value); err == nil && rt.AtLeast(ownershipFixed) {
			fixedRuntime = true
		}
	}
	if fixedRuntime {
		r := b.Res(probe.OK, 0.3, fmt.Sprintf("%d VHDX file(s) not owned by you; WSL %s handles this (fixed in 2.7.12)", len(foreign), e.Runtime.Version.Value))
		r.Detail = strings.Join(lines, "\n") + "\nRuntimes since 2.5.6 grant the VM access when attaching fails, and 2.7.11/2.7.12 fixed the ownership left by --move/--import."
		return r
	}
	r := b.Res(probe.Warn, 0.55, fmt.Sprintf("VHDX for %s is not owned by the current user", strings.Join(foreign, ", ")))
	r.Detail = strings.Join(lines, "\n") + "\nAttaching a disk owned by another account can fail with ERROR_ACCESS_DENIED (Wsl/Service/CreateInstance/AttachDisk/E_ACCESSDENIED), typically after wsl --move or --import across volumes."
	r.FixID, r.FixHint = "update", "wslkit doctor fix update   (2.7.12+ handles it)   or take ownership: takeown /F \"<path>\\ext4.vhdx\""
	r.Refs = []string{"https://github.com/microsoft/WSL/releases/tag/2.7.12", "https://github.com/microsoft/WSL/releases/tag/2.7.11"}
	return r
}
