// Package perf holds memory and CPU probes (MEM***).
package perf

import (
	"fmt"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
	"github.com/wslkit/wsldoctor/internal/wslconfig"
)

func All() []probe.Probe { return []probe.Probe{Memory{}} }

const gib = 1 << 30

type Memory struct{}

func (Memory) base() probe.Base {
	return probe.Base{PID: "MEM001", PTitle: "WSL VM memory cap vs host RAM", PMilestone: "M1", PNeeds: []string{"WSL002"}}
}
func (p Memory) ID() string        { return p.base().ID() }
func (p Memory) Title() string     { return p.base().Title() }
func (p Memory) Milestone() string { return p.base().Milestone() }
func (p Memory) Needs() []string   { return p.base().Needs() }

func (p Memory) Run(e *env.Env) probe.Result {
	b := p.base()
	total := e.Host.TotalMemoryBytes
	var cfg *wslconfig.Config
	if e.Config.WslConfig.OK() {
		cfg = wslconfig.Parse(e.Config.WslConfig.Value)
	}
	var capBytes uint64
	capSrc := "default (50% of host RAM)"
	if cfg != nil {
		if v, ok := cfg.Get("wsl2", "memory"); ok {
			if n, ok := wslconfig.ParseSize(v); ok {
				capBytes, capSrc = n, ".wslconfig memory="+v
			} else {
				r := b.Res(probe.Warn, 0.5, fmt.Sprintf(".wslconfig memory=%q is not a valid size; WSL ignores it and uses the default", v))
				r.FixID, r.FixHint = "wslconfig", "use e.g. memory=4GB"
				return r
			}
		}
	}
	if capBytes == 0 && total.OK() {
		capBytes = total.Value / 2
	}
	reclaim := ""
	if cfg != nil {
		reclaim, _ = cfg.Get("experimental", "autoMemoryReclaim")
	}

	detail := fmt.Sprintf("Cap: %s (%s)", human(capBytes), capSrc)
	if total.OK() {
		detail += fmt.Sprintf("\nHost RAM: %s", human(total.Value))
	}
	ws := e.Procs.VmmemWorkingSet
	switch {
	case ws.OK():
		detail += fmt.Sprintf("\n%s working set now: %s", e.Procs.VmmemName, human(ws.Value))
	case ws.Absent():
		detail += "\nWSL VM is not running"
	case ws.NeedsElevation():
		detail += "\nVM working set needs elevation to read"
	}
	if reclaim != "" {
		detail += "\nautoMemoryReclaim=" + reclaim
	}

	if ws.OK() && capBytes > 0 && ws.Value > capBytes*9/10 {
		r := b.Res(probe.Warn, 0.5, fmt.Sprintf("WSL VM is using %s of its %s cap", human(ws.Value), human(capBytes)))
		r.Detail = detail + "\nLinux page cache counts against the cap and is not returned to Windows until the VM idles or autoMemoryReclaim runs."
		r.FixID, r.FixHint = "wslconfig", "add to .wslconfig: [experimental] autoMemoryReclaim=gradual   or lower [wsl2] memory="
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/4166"}
		return r
	}
	if total.OK() && total.Value <= 8*gib && capSrc[:7] == "default" {
		r := b.Res(probe.Warn, 0.3, fmt.Sprintf("Low-RAM host (%s) with the default cap of %s", human(total.Value), human(capBytes)))
		r.Detail = detail + "\nOn small machines the 50% default leaves Windows starved when WSL fills its page cache."
		r.FixID, r.FixHint = "wslconfig", "add to .wslconfig: [wsl2] memory=3GB and [experimental] autoMemoryReclaim=gradual"
		return r
	}
	r := b.Res(probe.OK, 0.4, fmt.Sprintf("VM memory cap %s", human(capBytes)))
	r.Detail = detail
	return r
}

func human(b uint64) string {
	if b >= gib {
		return fmt.Sprintf("%.1f GB", float64(b)/gib)
	}
	return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
}
