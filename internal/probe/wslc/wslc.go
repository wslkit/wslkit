// Package wslc holds probes (WSC***) for wslc, the container CLI in the WSL 2.9
// pre-releases. Asking a wslc session anything boots its VM, so only sessions
// whose VM is already running are tested, and a stopped one is left stopped.
package wslc

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func All() []probe.Probe { return []probe.Probe{DNS{}} }

// ---------------------------------------------------------------- WSC001

// DNS checks that wslc containers can resolve names.
//
// A wslc session's VM gets its network from WSL's user-mode device host, not
// from the NAT the distributions use, and hands its containers the host's
// IPv4 resolver. Measured on Windows 10 19045 with WSL 2.9.12: a query to that
// resolver from inside the session VM came back SERVFAIL at once and never
// reached the network card, while the same query from Windows was answered
// and a query to a public resolver worked. Every container then fails to
// resolve anything, and nothing else on the machine looks wrong: the
// distributions, and Docker inside them, use WSL's own DNS proxy.
type DNS struct{}

func (DNS) base() probe.Base {
	return probe.Base{PID: "WSC001", PTitle: "wslc containers can resolve names", PMilestone: "M3"}
}
func (p DNS) ID() string        { return p.base().ID() }
func (p DNS) Title() string     { return p.base().Title() }
func (p DNS) Milestone() string { return p.base().Milestone() }
func (p DNS) Needs() []string   { return p.base().Needs() }

func (p DNS) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.WSLC.Collected() {
		return b.Res(probe.Skipped, 0, "skipped: wslc was not checked (older snapshot)")
	}
	if e.WSLC.Absent() {
		return b.Res(probe.Skipped, 0, "skipped: wslc is not installed; it ships with the WSL 2.9 pre-releases")
	}
	if !e.WSLC.OK() {
		return b.Res(probe.Unknown, 0.1, "could not ask wslc about its sessions: "+e.WSLC.Err)
	}
	all := e.WSLC.Value.Sessions
	if len(all) == 0 {
		return b.Res(probe.Skipped, 0, "skipped: no wslc session exists yet, so there are no containers to check")
	}
	// Only a running session was tested; a stopped one was left stopped.
	var sessions []env.WSLCSession
	for _, s := range all {
		if s.Running || s.Err != "" {
			sessions = append(sessions, s)
		}
	}
	if len(sessions) == 0 {
		r := b.Res(probe.Skipped, 0, "skipped: no wslc session VM is running, and doctor does not start one")
		r.Detail = "Testing a session means running a command in its VM, which would start it. Run doctor while a wslc container is up to check its DNS."
		return r
	}

	var broken, unknown []string
	var lines []string
	for _, s := range sessions {
		if s.Err != "" {
			unknown = append(unknown, fmt.Sprintf("%s: %s", s.Name, s.Err))
			continue
		}
		lines = append(lines, describe(s))
		if msg, ok := verdict(s); !ok {
			broken = append(broken, msg)
		}
	}
	detail := strings.Join(lines, "\n")

	if len(broken) > 0 {
		r := b.Res(probe.Warn, 0.8, "wslc containers cannot resolve names: "+strings.Join(broken, "; "))
		if publicAnswered(sessions) {
			r.Detail = detail + "\n\n" +
				"A container is given the session VM's IPv4 resolvers, and those failed while " + publicOf(sessions) + " answered from the same VM. " +
				"On Windows 10 with WSL 2.9.12 this was measured as a SERVFAIL made on the PC itself: the query to the host's resolver never reached the network card. " +
				"Distributions, and Docker inside them, are not affected: they use WSL's own DNS proxy."
			r.FixHint = "give the container a resolver that works: wslc run --dns " + publicAddr(sessions) + " ..."
		} else {
			r.Confidence = 0.5
			r.Detail = detail + "\n\nNo resolver answered from the session VM, not even a public one, so this looks like no network in the session VM rather than a DNS fault."
			r.FixHint = "check the host's own connection first; wslc's VM shares it"
		}
		r.Refs = []string{"https://github.com/wslkit/wslkit/blob/main/docs/research/2026-09-wslc-session.md"}
		return r
	}
	if len(unknown) > 0 {
		r := b.Res(probe.Unknown, 0.2, "could not test DNS in "+strings.Join(unknown, "; "))
		r.Detail = detail
		return r
	}
	r := b.Res(probe.OK, 0.6, fmt.Sprintf("DNS works for containers in %d wslc session(s)", len(sessions)))
	r.Detail = detail
	return r
}

// verdict says whether a session's containers can resolve names, and if not,
// why, in a phrase.
func verdict(s env.WSLCSession) (string, bool) {
	if len(s.Resolvers) == 0 {
		return fmt.Sprintf("session %s gives containers no IPv4 resolver", s.Name), false
	}
	for _, r := range s.Resolvers {
		if answered(r) {
			return "", true
		}
	}
	var failed []string
	for _, r := range s.Resolvers {
		failed = append(failed, fmt.Sprintf("%s answers %s", r.Addr, r.Status))
	}
	msg := fmt.Sprintf("in session %s, %s", s.Name, strings.Join(failed, ", "))
	if !answered(s.Public) {
		msg += fmt.Sprintf(", and %s does not answer either, so the session VM may have no network at all", s.Public.Addr)
	}
	return msg, false
}

// answered is true for a resolver that gave a real answer. NXDOMAIN is an
// answer too; the name asked for exists, but a resolver that says it does
// not is still resolving.
func answered(r env.WSLCResolver) bool {
	return r.Status == "NOERROR" || r.Status == "NXDOMAIN"
}

func describe(s env.WSLCSession) string {
	var parts []string
	for _, r := range s.Resolvers {
		parts = append(parts, fmt.Sprintf("%s %s", r.Addr, r.Status))
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}
	return fmt.Sprintf("session %s: containers get %s; %s %s", s.Name, strings.Join(parts, ", "), s.Public.Addr, s.Public.Status)
}

func publicAnswered(sessions []env.WSLCSession) bool {
	for _, s := range sessions {
		if answered(s.Public) {
			return true
		}
	}
	return false
}

func publicAddr(sessions []env.WSLCSession) string {
	for _, s := range sessions {
		if answered(s.Public) {
			return s.Public.Addr
		}
	}
	return "1.1.1.1"
}

func publicOf(sessions []env.WSLCSession) string {
	for _, s := range sessions {
		if s.Public.Addr != "" {
			return "a public resolver (" + s.Public.Addr + ")"
		}
	}
	return "a public resolver"
}
