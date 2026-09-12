// Package disk holds VHDX and host-volume probes (DSK***).
package disk

import (
	"fmt"
	"strings"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
)

func All() []probe.Probe {
	return []probe.Probe{Integrity{}, Reclaimable{}, Attributes{}, HostFree{}, Ownership{}}
}

const gib = 1 << 30

func human(b uint64) string {
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GB", float64(b)/gib)
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ---------------------------------------------------------------- DSK002

type Integrity struct{}

func (Integrity) base() probe.Base {
	return probe.Base{PID: "DSK002", PTitle: "VHDX presence and header integrity", PMilestone: "M1", PNeeds: []string{"WSL002"}}
}
func (p Integrity) ID() string        { return p.base().ID() }
func (p Integrity) Title() string     { return p.base().Title() }
func (p Integrity) Milestone() string { return p.base().Milestone() }
func (p Integrity) Needs() []string   { return p.base().Needs() }

func (p Integrity) Run(e *env.Env) probe.Result {
	b := p.base()
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.Skipped, 0, "skipped: no distributions")
	}
	var lines, bad []string
	for _, d := range distros {
		if d.Version == 1 {
			lines = append(lines, d.Name+": WSL 1, no VHDX")
			continue
		}
		v := d.Vhd
		switch {
		case v.Absent():
			bad = append(bad, fmt.Sprintf("%s: %s does not exist", d.Name, v.Source))
		case v.NeedsElevation():
			lines = append(lines, d.Name+": VHDX not readable without elevation")
		case !v.OK():
			bad = append(bad, fmt.Sprintf("%s: cannot open VHDX: %s", d.Name, v.Err))
		case v.Value.FileSize == 0:
			bad = append(bad, fmt.Sprintf("%s: VHDX is zero bytes", d.Name))
		case !v.Value.MagicOK:
			bad = append(bad, fmt.Sprintf("%s: file does not start with 'vhdxfile' (%s)", d.Name, v.Value.ParseErr))
		case !v.Value.HeaderOK:
			bad = append(bad, fmt.Sprintf("%s: both VHDX headers fail checksum (%s)", d.Name, v.Value.ParseErr))
		default:
			lines = append(lines, fmt.Sprintf("%s: %s on disk, virtual size %s, headers valid", d.Name, human(v.Value.FileSize), human(v.Value.VirtualSize)))
		}
	}
	if len(bad) > 0 {
		r := b.Res(probe.Fail, 0.85, bad[0])
		r.Detail = strings.Join(append(bad, lines...), "\n") + "\nThis presents as: launch fails immediately, or 'The system cannot find the file specified'."
		r.FixHint = "if the file was moved, fix BasePath in HKCU\\...\\Lxss\\{guid}; otherwise restore from backup or wsl --unregister and reinstall"
		return r
	}
	r := b.Res(probe.OK, 0.6, "All distro VHDX files present with valid headers")
	r.Detail = strings.Join(lines, "\n")
	return r
}

// ---------------------------------------------------------------- DSK001

type Reclaimable struct{}

func (Reclaimable) base() probe.Base {
	return probe.Base{PID: "DSK001", PTitle: "VHDX space reclaimable", PMilestone: "M1", PNeeds: []string{"DSK002"}}
}
func (p Reclaimable) ID() string        { return p.base().ID() }
func (p Reclaimable) Title() string     { return p.base().Title() }
func (p Reclaimable) Milestone() string { return p.base().Milestone() }
func (p Reclaimable) Needs() []string   { return p.base().Needs() }

func (p Reclaimable) Run(e *env.Env) probe.Result {
	b := p.base()
	var lines []string
	var total uint64
	for _, d := range e.DistroList() {
		v := d.Vhd
		if !v.OK() || v.Value.BlockSize == 0 {
			continue
		}
		file := v.Value.FileSize
		alloc := v.Value.AllocatedBytes
		if file <= alloc {
			lines = append(lines, fmt.Sprintf("%s: %s on disk, %s allocated", d.Name, human(file), human(alloc)))
			continue
		}
		// Metadata, BAT and log account for a few MiB; ignore below 64 MiB.
		slack := file - alloc
		if slack < 64<<20 {
			lines = append(lines, fmt.Sprintf("%s: %s on disk, %s allocated, compact", d.Name, human(file), human(alloc)))
			continue
		}
		total += slack
		lines = append(lines, fmt.Sprintf("%s: %s on disk but only %s allocated to blocks; ~%s reclaimable", d.Name, human(file), human(alloc), human(slack)))
	}
	if len(lines) == 0 {
		return b.Res(probe.Skipped, 0, "skipped: no readable WSL 2 VHDX")
	}
	if total >= 1*gib {
		r := b.Res(probe.Warn, 0.35, fmt.Sprintf("~%s of host disk is held by VHDX files that no longer back allocated blocks", human(total)))
		r.Detail = strings.Join(lines, "\n") + "\nNote: allocated blocks can still contain deleted-file space inside ext4; the true reclaimable amount is usually larger."
		r.FixHint = "wsldisk compact <distro>    (https://github.com/wslkit/wsldisk)   or: wsl --manage <distro> --set-sparse true"
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/4699"}
		return r
	}
	r := b.Res(probe.OK, 0.4, "VHDX files are close to their allocated size")
	r.Detail = strings.Join(lines, "\n")
	return r
}

// ---------------------------------------------------------------- DSK003

type Attributes struct{}

func (Attributes) base() probe.Base {
	return probe.Base{PID: "DSK003", PTitle: "VHDX compressed / encrypted / sparse attributes", PMilestone: "M2", PNeeds: []string{"DSK002"}}
}
func (p Attributes) ID() string        { return p.base().ID() }
func (p Attributes) Title() string     { return p.base().Title() }
func (p Attributes) Milestone() string { return p.base().Milestone() }
func (p Attributes) Needs() []string   { return p.base().Needs() }

func (p Attributes) Run(e *env.Env) probe.Result {
	b := p.base()
	var bad, notes []string
	seen := 0
	for _, d := range e.DistroList() {
		v := d.Vhd
		if !v.OK() {
			continue
		}
		seen++
		if v.Value.Compressed {
			bad = append(bad, fmt.Sprintf("%s: VHDX has the NTFS compressed attribute", d.Name))
		}
		if v.Value.Encrypted {
			bad = append(bad, fmt.Sprintf("%s: VHDX has the EFS encrypted attribute", d.Name))
		}
		if v.Value.Sparse {
			notes = append(notes, fmt.Sprintf("%s: VHDX is sparse (fine if set by WSL via sparseVhd / --set-sparse)", d.Name))
		}
	}
	if seen == 0 {
		return b.Res(probe.Skipped, 0, "skipped: no readable WSL 2 VHDX")
	}
	if len(bad) > 0 {
		r := b.Res(probe.Fail, 0.8, bad[0])
		r.Detail = strings.Join(append(bad, notes...), "\n") + "\nWSL refuses compressed or encrypted VHDX files. This presents as: 'The virtual hard disk file must be uncompressed and unencrypted and must not be sparse' (0x80070570 / HCS_E_...)."
		r.FixHint = "compact /u <path>\\ext4.vhdx   and   cipher /d <path>\\ext4.vhdx   (then check the folder's attributes too)"
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/4103"}
		return r
	}
	if len(notes) > 0 {
		r := b.Res(probe.OK, 0.4, "No compressed or encrypted VHDX")
		r.Detail = strings.Join(notes, "\n")
		return r
	}
	return b.Res(probe.OK, 0.4, "No compressed, encrypted or sparse VHDX attributes")
}

// ---------------------------------------------------------------- DSK005

type HostFree struct{}

func (HostFree) base() probe.Base {
	return probe.Base{PID: "DSK005", PTitle: "Free space on the volume holding each VHDX", PMilestone: "M2", PNeeds: []string{"WSL002"}}
}
func (p HostFree) ID() string        { return p.base().ID() }
func (p HostFree) Title() string     { return p.base().Title() }
func (p HostFree) Milestone() string { return p.base().Milestone() }
func (p HostFree) Needs() []string   { return p.base().Needs() }

func (p HostFree) Run(e *env.Env) probe.Result {
	b := p.base()
	var lines, low []string
	for _, d := range e.DistroList() {
		if !d.VolumeFree.OK() {
			continue
		}
		free := d.VolumeFree.Value
		lines = append(lines, fmt.Sprintf("%s: %s free on its volume", d.Name, human(free)))
		if free < 2*gib {
			low = append(low, fmt.Sprintf("%s: only %s free; WSL wedges when the VHDX cannot grow", d.Name, human(free)))
		}
	}
	if len(lines) == 0 {
		return b.Res(probe.Skipped, 0, "skipped: volume free space not collected")
	}
	if len(low) > 0 {
		r := b.Res(probe.Warn, 0.6, low[0])
		r.Detail = strings.Join(lines, "\n") + "\nSymptoms: distro hangs, ext4 goes read-only, 'No space left on device' inside Linux while df shows space."
		r.FixHint = "free host disk space, then: wsldisk compact <distro>"
		return r
	}
	r := b.Res(probe.OK, 0.4, "Host volumes have headroom")
	r.Detail = strings.Join(lines, "\n")
	return r
}
