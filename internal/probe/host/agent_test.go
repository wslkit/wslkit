package host

import (
	"strings"
	"testing"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func TestAgentDaemon(t *testing.T) {
	e := env.New("t")
	if r := (AgentDaemon{}).Run(e); r.Status != probe.Skipped {
		t.Fatalf("uncollected -> %s", r.Status)
	}
	e.Agent = env.Absent[env.AgentInfo]("p")
	if r := (AgentDaemon{}).Run(e); r.Status != probe.Skipped {
		t.Fatalf("absent -> %s", r.Status)
	}
	e.Agent = env.Ok(env.AgentInfo{PID: 4, Alive: false, UpdatedAt: time.Now()}, "p")
	if r := (AgentDaemon{}).Run(e); r.Status != probe.Warn || !strings.Contains(r.FixHint, "agent start") {
		t.Fatalf("dead -> %s %q", r.Status, r.FixHint)
	}
	e.Agent = env.Ok(env.AgentInfo{PID: 4, Alive: true, UpdatedAt: time.Now(), VMID: "x"}, "p")
	if r := (AgentDaemon{}).Run(e); r.Status != probe.Warn || !strings.Contains(r.Summary, "no distribution") {
		t.Fatalf("no guests -> %s %q", r.Status, r.Summary)
	}
	e.Agent = env.Ok(env.AgentInfo{PID: 4, Alive: true, UpdatedAt: time.Now(), Guests: []string{"Ubuntu"}}, "p")
	if r := (AgentDaemon{}).Run(e); r.Status != probe.OK || !strings.Contains(r.Summary, "Ubuntu") {
		t.Fatalf("healthy -> %s %q", r.Status, r.Summary)
	}
}
