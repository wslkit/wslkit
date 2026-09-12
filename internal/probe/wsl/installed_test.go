package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func TestInstalledPolicyDisabled(t *testing.T) {
	e := env.New("t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	e.Net.Policy = env.Ok(map[string]string{"AllowWSL": "0"}, "t")
	r := (Installed{}).Run(e)
	if r.Status != probe.Fail || !strings.Contains(r.Summary, "policy") || r.FixHint == "" {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
	e.Net.Policy.Value["AllowWSL"] = "1"
	if r := (Installed{}).Run(e); r.Status != probe.OK {
		t.Fatalf("allowed -> %s %q", r.Status, r.Summary)
	}
}

func TestInstalledVariants(t *testing.T) {
	e := env.New("t")
	e.Runtime.Version = env.Absent[string]("p")
	e.Runtime.InboxWslVersion = env.Ok("10.0.26100.1", "p")
	if r := (Installed{}).Run(e); r.Status != probe.Fail || !strings.Contains(r.Summary, "not installed") {
		t.Fatalf("stub only -> %s %q", r.Status, r.Summary)
	}
	e.Host.Services = env.Ok(map[string]env.Service{"LxssManager": {Exists: true, State: "Stopped"}}, "t")
	if r := (Installed{}).Run(e); r.Status != probe.Fail || !strings.Contains(r.Summary, "legacy") {
		t.Fatalf("legacy -> %s %q", r.Status, r.Summary)
	}
}
