package host

import (
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// ---------------------------------------------------------------- AGT001

// AgentDaemon reports the state of the wslkit guest-agent daemon: not
// installed, installed but stopped, running without guests, or healthy.
type AgentDaemon struct{}

func (AgentDaemon) base() probe.Base {
	return probe.Base{PID: "AGT001", PTitle: "wslkit guest agent daemon", PMilestone: "M2"}
}
func (p AgentDaemon) ID() string        { return p.base().ID() }
func (p AgentDaemon) Title() string     { return p.base().Title() }
func (p AgentDaemon) Milestone() string { return p.base().Milestone() }
func (p AgentDaemon) Needs() []string   { return nil }

func (p AgentDaemon) Run(e *env.Env) probe.Result {
	b := p.base()
	f := e.Agent
	switch {
	case !f.Collected():
		return b.Res(probe.Skipped, 0, "skipped: agent status not collected (older snapshot)")
	case f.Absent():
		return b.Res(probe.Skipped, 0, "skipped: the guest agent daemon has never run on this machine")
	case !f.OK():
		return b.Res(probe.Unknown, 0.1, "could not read the agent status file: "+f.Err)
	}
	a := f.Value
	detail := fmt.Sprintf("pid %d alive=%v, version %s, vm %s, %d allowed target(s), autostart=%v", a.PID, a.Alive, a.Version, orDash(a.VMID), a.AllowedNum, a.Autostart)
	if a.LastError != "" {
		detail += "\nlast error: " + a.LastError
	}
	if !a.Alive {
		r := b.Res(probe.Warn, 0.5, "The wslkit agent daemon is not running; bridged sockets (ssh-agent, ...) will refuse connections")
		r.Detail = detail
		r.FixHint = "wslkit agent start      (wslkit agent autostart on to start it at logon)"
		return r
	}
	if len(a.Guests) == 0 {
		age := time.Since(a.UpdatedAt).Round(time.Second)
		r := b.Res(probe.Warn, 0.4, "The wslkit agent daemon is running but no distribution is connected")
		r.Detail = detail + fmt.Sprintf("\nstatus last updated %s ago", age)
		if a.VMID == "" {
			r.Detail += "\nNo running WSL VM was found: the daemon binds when a distro starts."
		} else {
			r.Detail += "\nInside the distro: systemctl status wslkit-agent   (install with: wslkit agent install -d <distro>)"
		}
		r.FixHint = "wslkit agent status; wslkit agent install -d <distro>"
		return r
	}
	r := b.Res(probe.OK, 0.5, fmt.Sprintf("Agent daemon running; %d guest(s) connected: %s", len(a.Guests), strings.Join(a.Guests, ", ")))
	r.Detail = detail
	return r
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
