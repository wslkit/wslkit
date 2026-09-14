package disk

import (
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/ext4"
	"github.com/wslkit/wslkit/internal/probe"
)

// ---------------------------------------------------------------- DSK004

// Filesystem reports what the guest's own ext4 superblock says about itself.
//
// This is the one question a stopped distribution still answers. The kernel
// records every error it hits in the superblock — a count, and the first and
// last with the function that reported them — and only fsck clears them, so a
// corruption that stopped a distribution from booting is still written down in
// the file afterwards. Reading it costs one 1 KiB read at a fixed offset in the
// VHDX and starts nothing.
type Filesystem struct{}

func (Filesystem) base() probe.Base {
	return probe.Base{PID: "DSK004", PTitle: "ext4 errors recorded in the guest filesystem", PMilestone: "M3", PNeeds: []string{"DSK002"}}
}
func (p Filesystem) ID() string        { return p.base().ID() }
func (p Filesystem) Title() string     { return p.base().Title() }
func (p Filesystem) Milestone() string { return p.base().Milestone() }
func (p Filesystem) Needs() []string   { return p.base().Needs() }

func (p Filesystem) Run(e *env.Env) probe.Result {
	b := p.base()
	var bad, lines, unreadable []string
	read := 0
	for _, d := range e.DistroList() {
		if d.Version == 1 {
			continue
		}
		f := d.Ext4
		switch {
		case !f.Collected():
			continue
		case f.Absent():
			// Not ext4: a disk this has no business judging.
			lines = append(lines, d.Name+": not an ext4 filesystem")
		case f.NeedsElevation():
			unreadable = append(unreadable, d.Name+": superblock not readable without elevation")
		case !f.OK():
			unreadable = append(unreadable, fmt.Sprintf("%s: cannot read the superblock: %s", d.Name, f.Err))
		default:
			read++
			s := f.Value
			if s.HasErrors || s.ErrorCount > 0 {
				bad = append(bad, fmt.Sprintf("%s: %s", d.Name, errorSummary(s.ErrorCount, s.HasErrors)))
				bad = append(bad, indent(fsDetail(d, s))...)
				continue
			}
			lines = append(lines, fmt.Sprintf("%s: no errors recorded%s", d.Name, checkedWhen(s.LastCheck)))
		}
	}
	switch {
	case len(bad) > 0:
		r := b.Res(probe.Fail, 0.9, bad[0])
		r.Detail = strings.Join(append(bad[1:], lines...), "\n") +
			"\nThis presents as: Wsl/Service/E_UNEXPECTED at launch, /sbin/init failing to load a shared library, or a distribution that mounts read-only." +
			"\nThe count is only cleared by fsck, so it can describe damage the kernel has already worked around; the times say how recent it is."
		r.FixHint = "export the distribution first (wsl --export <distro> backup.tar), then check its filesystem read-only from the system distro: wsl --system -u root -- e2fsck -n <device>"
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/13484"}
		return r
	case read > 0:
		r := b.Res(probe.OK, 0.5, "No ext4 errors recorded in any distribution's superblock")
		r.Detail = strings.Join(append(lines, unreadable...), "\n")
		return r
	case len(unreadable) > 0:
		r := b.Res(probe.Unknown, 0.1, "No distribution's superblock could be read")
		r.Detail = strings.Join(unreadable, "\n")
		for _, u := range unreadable {
			if strings.Contains(u, "elevation") {
				r.Elevate = true
				r.FixHint = "wslkit doctor check --elevated   (from an elevated terminal)"
				break
			}
		}
		return r
	default:
		return b.Res(probe.Skipped, 0, "skipped: no readable WSL 2 ext4 filesystem")
	}
}

func errorSummary(count uint32, marked bool) string {
	switch {
	case count == 1:
		return "the ext4 filesystem has recorded 1 error"
	case count > 1:
		return fmt.Sprintf("the ext4 filesystem has recorded %d errors", count)
	case marked:
		return "the kernel marked the ext4 filesystem as containing errors"
	}
	return "the ext4 filesystem is marked as having errors"
}

// fsDetail is the evidence behind the summary, in the order somebody reads it:
// what the kernel said, when, and whether anything has checked it since.
func fsDetail(d env.Distro, s ext4.Super) []string {
	var out []string
	if e := s.First.String(); e != "" {
		out = append(out, "first error: "+e)
	}
	if e := s.Last.String(); e != "" && e != s.First.String() {
		out = append(out, "last error:  "+e)
	}
	if s.LastCheck > 0 {
		out = append(out, "last fsck:   "+time.Unix(s.LastCheck, 0).UTC().Format("2006-01-02 15:04 UTC"))
	}
	if !s.Clean {
		out = append(out, "the filesystem was not cleanly unmounted")
	}
	if d.Running.OK() && d.Running.Value {
		out = append(out, "read from the file while the distribution is running, so the kernel's own copy may be newer")
	}
	return out
}

func checkedWhen(lastCheck int64) string {
	if lastCheck <= 0 {
		return ""
	}
	return ", last checked " + time.Unix(lastCheck, 0).UTC().Format("2006-01-02")
}

func indent(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, "  "+l)
	}
	return out
}
