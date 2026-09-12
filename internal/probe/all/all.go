// Package all lists every probe in execution order. Dependencies (Needs) must
// point at probes that appear earlier in this list.
package all

import (
	"github.com/wslkit/wsldoctor/internal/probe"
	"github.com/wslkit/wsldoctor/internal/probe/disk"
	"github.com/wslkit/wsldoctor/internal/probe/evt"
	"github.com/wslkit/wsldoctor/internal/probe/host"
	"github.com/wslkit/wsldoctor/internal/probe/perf"
	"github.com/wslkit/wsldoctor/internal/probe/wsl"
)

func Probes() []probe.Probe {
	var out []probe.Probe
	out = append(out, host.All()...) // HST001-003, DEF001, PLG001: host facts first
	out = append(out, wsl.All()...)  // WSL002, WSL001, WSL003, WSL004
	out = append(out, disk.All()...) // DSK002, DSK001, DSK003, DSK005
	out = append(out, perf.All()...) // MEM001
	out = append(out, evt.All()...)  // EVT001
	return out
}
