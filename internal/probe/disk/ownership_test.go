package disk

import (
	"strings"
	"testing"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
)

func vhdEnv(runtime, owner string) *env.Env {
	e := env.New("t")
	e.Runtime.Version = env.Ok(runtime, "t")
	e.Distros = env.Ok([]env.Distro{{Name: "u", Version: 2, Vhd: env.Ok(env.VhdInfo{Path: `C:\x\ext4.vhdx`, Owner: owner, OwnerSID: "S-1-5-32-544"}, "t")}}, "t")
	return e
}

func TestOwnership(t *testing.T) {
	if r := (Ownership{}).Run(vhdEnv("2.7.13.0", "")); r.Status != probe.Skipped {
		t.Fatalf("uncollected -> %s", r.Status)
	}
	if r := (Ownership{}).Run(vhdEnv("2.4.13.0", env.OwnerCurrentUser)); r.Status != probe.OK {
		t.Fatalf("own -> %s", r.Status)
	}
	r := (Ownership{}).Run(vhdEnv("2.4.13.0", env.OwnerAdministrators))
	if r.Status != probe.Warn || !strings.Contains(r.Detail, "E_ACCESSDENIED") || r.FixID != "update" {
		t.Fatalf("admins on old runtime -> %s %q", r.Status, r.Detail)
	}
	if r := (Ownership{}).Run(vhdEnv("2.7.13.0", env.OwnerAdministrators)); r.Status != probe.OK || !strings.Contains(r.Summary, "2.7.12") {
		t.Fatalf("admins on fixed runtime -> %s %q", r.Status, r.Summary)
	}
}
