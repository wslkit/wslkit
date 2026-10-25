package net

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslver"
)

// ---------------------------------------------------------------- NET005

// MirroredPorts reports whether mirrored networking can reserve the ports it
// asks Windows for.
//
// It asks HNS for one contiguous run of 4096 TCP and UDP ports for the VM's
// ephemeral range (WslCoreGuestNetworkService.cpp). Up to 3.0.3 a refusal
// failed the whole configuration with 0x8007054f, and the VM started with no
// network at all; 3.0.4 halves the request down to 512 ports and says so
// (microsoft/WSL#41880). How HNS chooses the run is not documented; the
// report the fix came from traced the refusal to WinNAT's exclusions
// fragmenting the dynamic range (microsoft/WSL#41812), and that is what is
// measured here. So this finds the likely cause; it cannot see ports that are
// merely in use, which HNS also steps around.
type MirroredPorts struct{}

func (MirroredPorts) base() probe.Base {
	return probe.Base{PID: "NET005", PTitle: "Mirrored networking can reserve its port range", PMilestone: "M3", PNeeds: []string{"NET004"}}
}
func (p MirroredPorts) ID() string        { return p.base().ID() }
func (p MirroredPorts) Title() string     { return p.base().Title() }
func (p MirroredPorts) Milestone() string { return p.base().Milestone() }
func (p MirroredPorts) Needs() []string   { return p.base().Needs() }

const (
	// portsWanted is what WSL asks for first, portsMinimum the last it
	// tries from 3.0.4 (4095 and 511 in the source, which count the gaps
	// between the first and last port).
	portsWanted  = 4096
	portsMinimum = 512
)

// portsRetry is the first runtime that tries smaller ranges.
var portsRetry = wslver.MustParse("3.0.4")

func (p MirroredPorts) Run(e *env.Env) probe.Result {
	b := p.base()
	if mode, _, _ := Mode(e); mode != "mirrored" {
		return b.Res(probe.Skipped, 0, "Only matters with networkingMode=mirrored; this host uses "+mode)
	}
	if !e.Net.TCPPorts.Collected() || !e.Net.UDPPorts.Collected() {
		return b.Res(probe.Skipped, 0, "skipped: port ranges not collected (older snapshot)")
	}
	for _, f := range []env.Field[env.PortPool]{e.Net.TCPPorts, e.Net.UDPPorts} {
		if !f.OK() {
			return b.Res(probe.Unknown, 0.1, "could not read the port ranges: "+f.Err)
		}
	}
	tcp, udp := largestFree(e.Net.TCPPorts.Value), largestFree(e.Net.UDPPorts.Value)
	free := min(tcp.End-tcp.Start+1, udp.End-udp.Start+1)
	if tcp.End < tcp.Start || udp.End < udp.Start {
		free = 0
	}
	lines := []string{
		describePool("TCP", e.Net.TCPPorts.Value, tcp),
		describePool("UDP", e.Net.UDPPorts.Value, udp),
		"HNS's choice of range is not documented: this measures the fragmentation the report in microsoft/WSL#41812 traced, and cannot see ports merely in use. If this is clear and mirrored mode still falls back, look there.",
	}
	refs := []string{"https://github.com/microsoft/WSL/issues/41812", "https://github.com/microsoft/WSL/pull/41880"}
	hint := "the exclusions without an asterisk were added at runtime, by WinNAT and Hyper-V; the usual way to release them is net stop winnat then net start winnat from an elevated prompt, then wsl --shutdown and start WSL again"

	retries := false
	if rt, ok := runtimeVersion(e); ok && rt.AtLeast(portsRetry) {
		retries = true
	}
	switch {
	case free >= portsWanted:
		r := b.Res(probe.OK, 0.3, fmt.Sprintf("The dynamic port ranges have a free run of %d ports; mirrored networking asks for %d", free, portsWanted))
		r.Detail = strings.Join(lines, "\n")
		return r
	case retries && free >= portsMinimum:
		r := b.Res(probe.Warn, 0.45, fmt.Sprintf("The longest free run of ports is %d: WSL falls back to a smaller range than the %d it asks for", free, portsWanted))
		r.Detail = strings.Join(append(lines, "WSL 3.0.4 and later halve the request until it fits, down to 512, and print \"Mirrored networking is using a smaller ephemeral port range\". Fewer ports means fewer connections the guest can open at once."), "\n")
		r.FixHint, r.Refs = hint, refs
		return r
	default:
		need := portsWanted
		if retries {
			need = portsMinimum
		}
		r := b.Res(probe.Fail, 0.65, fmt.Sprintf("The longest free run of ports is %d, short of the %d mirrored networking needs: it is likely to fail and leave the VM without network", free, need))
		what := "Up to WSL 3.0.3 the reservation is all or nothing: it fails with CreateInstance/CreateVm/ConfigureNetworking/0x8007054f and the VM starts with networkingMode=None."
		if retries {
			what = "WSL 3.0.4 and later print \"Mirrored networking could not reserve the minimum contiguous range of 512 TCP and UDP ports\" and the configuration fails."
		}
		r.Detail = strings.Join(append(lines, what), "\n")
		r.FixHint, r.Refs = hint, refs
		return r
	}
}

// largestFree is the longest run of the dynamic range that no exclusion
// covers. An empty result has End < Start.
func largestFree(p env.PortPool) env.PortSpan {
	ex := append([]env.PortSpan(nil), p.Excluded...)
	sort.Slice(ex, func(i, j int) bool { return ex[i].Start < ex[j].Start })
	best := env.PortSpan{Start: 1, End: 0}
	next := p.Dynamic.Start
	consider := func(end int) {
		if end >= next && end-next > best.End-best.Start {
			best = env.PortSpan{Start: next, End: end}
		}
	}
	for _, x := range ex {
		if x.End < next || x.Start > p.Dynamic.End {
			continue
		}
		consider(min(x.Start-1, p.Dynamic.End))
		if x.End+1 > next {
			next = x.End + 1
		}
	}
	consider(p.Dynamic.End)
	return best
}

func describePool(proto string, p env.PortPool, free env.PortSpan) string {
	inside := 0
	for _, x := range p.Excluded {
		if x.End >= p.Dynamic.Start && x.Start <= p.Dynamic.End {
			inside++
		}
	}
	run := "none"
	if free.End >= free.Start {
		run = fmt.Sprintf("%d ports (%d-%d)", free.End-free.Start+1, free.Start, free.End)
	}
	return fmt.Sprintf("%s dynamic range %d-%d, %d exclusion(s) inside it, longest free run %s", proto, p.Dynamic.Start, p.Dynamic.End, inside, run)
}
