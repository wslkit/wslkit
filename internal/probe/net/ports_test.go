package net

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

var dynamic = env.PortSpan{Start: 49152, End: 65535}

func portsEnv(runtime string, tcp, udp []env.PortSpan) *env.Env {
	e := netEnv(26100, "[wsl2]\nnetworkingMode=mirrored\n")
	e.Runtime.Version = env.Ok(runtime, "t")
	e.Net.TCPPorts = env.Ok(env.PortPool{Dynamic: dynamic, Excluded: tcp}, "t")
	e.Net.UDPPorts = env.Ok(env.PortPool{Dynamic: dynamic, Excluded: udp}, "t")
	return e
}

// fragment leaves no free run longer than gap-1 ports anywhere in the range.
func fragment(gap int) []env.PortSpan {
	var out []env.PortSpan
	for p := dynamic.Start + gap - 1; p <= dynamic.End; p += gap {
		out = append(out, env.PortSpan{Start: p, End: p})
	}
	return out
}

func TestLargestFree(t *testing.T) {
	for _, tc := range []struct {
		name string
		ex   []env.PortSpan
		want env.PortSpan
	}{
		{"nothing excluded", nil, dynamic},
		{"as measured here", []env.PortSpan{{50000, 50059}, {56198, 56297}, {60694, 60793}}, env.PortSpan{Start: 50060, End: 56197}},
		{"unsorted and overlapping", []env.PortSpan{{60000, 65535}, {49152, 50000}, {49900, 52000}}, env.PortSpan{Start: 52001, End: 59999}},
		{"outside the range is ignored", []env.PortSpan{{1000, 2000}}, dynamic},
		{"all of it", []env.PortSpan{{40000, 65535}}, env.PortSpan{Start: 1, End: 0}},
	} {
		got := largestFree(env.PortPool{Dynamic: dynamic, Excluded: tc.ex})
		if got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestMirroredPorts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
		tcp     []env.PortSpan
		udp     []env.PortSpan
		want    probe.Status
		summary string
	}{
		{"as measured here", "3.0.3.0", []env.PortSpan{{50000, 50059}, {56198, 56297}, {60694, 60793}}, []env.PortSpan{{49561, 49660}, {50000, 50059}, {56103, 56202}}, probe.OK, "free run of 6138 ports"},
		{"3.0.3 cannot shrink (microsoft/WSL#41812)", "3.0.3.0", fragment(3000), nil, probe.Fail, "short of the 4096"},
		{"3.0.4 shrinks", "3.0.4.0", fragment(3000), nil, probe.Warn, "smaller range"},
		{"UDP counts too", "3.0.4.0", nil, fragment(1000), probe.Warn, "999"},
		{"3.0.4 below its minimum", "3.0.4.0", fragment(400), nil, probe.Fail, "short of the 512"},
	} {
		r := (MirroredPorts{}).Run(portsEnv(tc.runtime, tc.tcp, tc.udp))
		if r.Status != tc.want || !strings.Contains(r.Summary, tc.summary) {
			t.Errorf("%s: %s %q, want %s containing %q", tc.name, r.Status, r.Summary, tc.want, tc.summary)
		}
	}
}

func TestMirroredPortsOnlyForMirrored(t *testing.T) {
	e := portsEnv("3.0.3.0", fragment(100), nil)
	e.Config.WslConfig = env.Ok("[wsl2]\nnetworkingMode=nat\n", "t")
	if r := (MirroredPorts{}).Run(e); r.Status != probe.Skipped {
		t.Errorf("%s %q", r.Status, r.Summary)
	}
	e = portsEnv("3.0.3.0", nil, nil)
	e.Net.UDPPorts = env.Field[env.PortPool]{}
	if r := (MirroredPorts{}).Run(e); r.Status != probe.Skipped {
		t.Errorf("an older snapshot: %s %q", r.Status, r.Summary)
	}
}
