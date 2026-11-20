package disk

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslver"
)

// ---------------------------------------------------------------- DSK006

// Ownership flags VHDX files the current user cannot read or does not own.
//
// Reading is what matters from WSL 3.0.5: the service opens a distribution's
// disk while impersonating the user and asks for GENERIC_READ, and a disk the
// user cannot read fails to attach (microsoft/WSL#42048). Up to 3.0.4 the
// same open asked for no access at all, and the service granted the VM access
// itself, so such a disk started.
//
// Ownership is the older problem: WSL 2.7.11/2.7.12 fixed `wsl --move` /
// `--import` leaving disks owned by BUILTIN\Administrators, which made
// attaching them fail with ERROR_ACCESS_DENIED on older runtimes.
type Ownership struct{}

func (Ownership) base() probe.Base {
	return probe.Base{PID: "DSK006", PTitle: "VHDX file ownership and access", PMilestone: "M2", PNeeds: []string{"DSK002"}}
}
func (p Ownership) ID() string        { return p.base().ID() }
func (p Ownership) Title() string     { return p.base().Title() }
func (p Ownership) Milestone() string { return p.base().Milestone() }
func (p Ownership) Needs() []string   { return p.base().Needs() }

var (
	ownershipFixed = wslver.MustParse("2.7.12")
	// userReadRequired is the first runtime that opens a distribution's
	// disk as the user, for reading.
	userReadRequired = wslver.MustParse("3.0.5")
)

const unreadableHint = `give yourself access to each disk listed: icacls "<path>\ext4.vhdx" /grant "%USERNAME%:F" (from an elevated prompt if you do not own the file)`

func (p Ownership) Run(e *env.Env) probe.Result {
	b := p.base()
	var lines, foreign, unreadable []string
	seen := 0
	for _, d := range e.DistroList() {
		v := d.Vhd
		// A refused open still carries the owner and the read check.
		if !(v.OK() || v.NeedsElevation()) || (v.Value.Owner == "" && v.Value.UserReadable == nil) {
			continue
		}
		seen++
		line := fmt.Sprintf("%s: owner %s (%s)", d.Name, v.Value.Owner, v.Value.OwnerSID)
		if v.Value.Owner == "" {
			line = d.Name + ": owner not read"
		}
		if r := v.Value.UserReadable; r != nil && !*r {
			line += ", and you cannot read it: " + v.Value.Path
			unreadable = append(unreadable, d.Name)
		}
		lines = append(lines, line)
		if v.Value.Owner != "" && v.Value.Owner != env.OwnerCurrentUser {
			foreign = append(foreign, d.Name)
		}
	}
	if seen == 0 {
		return b.Res(probe.Skipped, 0, "skipped: VHDX ownership not collected")
	}

	rt, haveRT := wslver.Version{}, false
	if e.Runtime.Version.OK() {
		if v, err := wslver.Parse(e.Runtime.Version.Value); err == nil {
			rt, haveRT = v, true
		}
	}

	if len(unreadable) > 0 {
		names := strings.Join(unreadable, ", ")
		why := "WSL 3.0.5 and later open a distribution's disk as you, asking for read access, and refuse to attach a disk you cannot read: the start fails with \"Failed to attach disk '<path>' to WSL2: Access is denied.\" (microsoft/WSL#42048). Up to 3.0.4 that open asked for no access, and WSL granted the VM access itself, so the same disk started. A disk already attached to a running VM keeps working, even after wsl --terminate; the failure comes on the first start after the VM stops (wsl --shutdown, or idle timeout)."
		var r probe.Result
		if haveRT && rt.AtLeast(userReadRequired) {
			r = b.Res(probe.Fail, 0.8, fmt.Sprintf("You cannot read the VHDX of %s, and WSL %s will not attach it", names, e.Runtime.Version.Value))
		} else {
			r = b.Res(probe.Warn, 0.6, fmt.Sprintf("You cannot read the VHDX of %s: it starts on this WSL, and stops starting from 3.0.5", names))
		}
		r.Detail = strings.Join(append(lines, why), "\n")
		r.FixHint = unreadableHint
		r.Refs = []string{"https://github.com/microsoft/WSL/pull/42048"}
		return r
	}

	if len(foreign) == 0 {
		r := b.Res(probe.OK, 0.4, "All distro VHDX files are owned by the current user")
		r.Detail = strings.Join(lines, "\n")
		return r
	}
	if haveRT && rt.AtLeast(ownershipFixed) {
		r := b.Res(probe.OK, 0.3, fmt.Sprintf("%d VHDX file(s) not owned by you; WSL %s handles this (fixed in 2.7.12)", len(foreign), e.Runtime.Version.Value))
		r.Detail = strings.Join(lines, "\n") + "\nRuntimes since 2.5.6 grant the VM access when attaching fails, and 2.7.11/2.7.12 fixed the ownership left by --move/--import. From 3.0.5 that holds only while you can still read the file, which you can."
		return r
	}
	r := b.Res(probe.Warn, 0.55, fmt.Sprintf("VHDX for %s is not owned by the current user", strings.Join(foreign, ", ")))
	r.Detail = strings.Join(lines, "\n") + "\nAttaching a disk owned by another account can fail with ERROR_ACCESS_DENIED (Wsl/Service/CreateInstance/AttachDisk/E_ACCESSDENIED), typically after wsl --move or --import across volumes."
	r.FixID, r.FixHint = "update", "wslkit doctor fix update   (2.7.12+ handles it)   or take ownership: takeown /F \"<path>\\ext4.vhdx\""
	r.Refs = []string{"https://github.com/microsoft/WSL/releases/tag/2.7.12", "https://github.com/microsoft/WSL/releases/tag/2.7.11"}
	return r
}
