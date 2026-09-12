// Package all lists every probe in execution order. Dependencies (Needs) must
// point at probes that appear earlier in this list.
package all

import (
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/probe/disk"
	"github.com/wslkit/wslkit/internal/probe/evt"
	"github.com/wslkit/wslkit/internal/probe/host"
	"github.com/wslkit/wslkit/internal/probe/net"
	"github.com/wslkit/wslkit/internal/probe/perf"
	"github.com/wslkit/wslkit/internal/probe/wsl"
)

func Probes() []probe.Probe {
	var out []probe.Probe
	out = append(out, host.All()...) // HST001-004, DEF001, PLG001: host facts first
	out = append(out, wsl.All()...)  // WSL002, WSL001, WSL003, WSL004, WSL005
	out = append(out, host.COMClass{})
	out = append(out, disk.All()...) // DSK002, DSK001, DSK003, DSK005, DSK006
	out = append(out, net.All()...)  // NET004, NET002
	out = append(out, perf.All()...) // MEM001
	out = append(out, evt.All()...)  // EVT001
	return out
}

// ByID returns the probes with the given IDs plus everything they Need
// (transitively), preserving registration order so cascades still work.
func ByID(ids []string) []probe.Probe {
	all := Probes()
	byID := map[string]probe.Probe{}
	for _, p := range all {
		byID[p.ID()] = p
	}
	want := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		if want[id] {
			return
		}
		p, ok := byID[id]
		if !ok {
			return
		}
		want[id] = true
		for _, dep := range p.Needs() {
			visit(dep)
		}
	}
	for _, id := range ids {
		visit(id)
	}
	var out []probe.Probe
	for _, p := range all {
		if want[p.ID()] {
			out = append(out, p)
		}
	}
	return out
}
