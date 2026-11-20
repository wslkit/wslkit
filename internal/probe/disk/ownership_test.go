package disk

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
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

func unreadableEnv(runtime string, readable bool) *env.Env {
	e := vhdEnv(runtime, env.OwnerAdministrators)
	d := e.Distros.Value[0]
	v := d.Vhd.Value
	v.UserReadable = &readable
	d.Vhd = env.Ok(v, "t")
	e.Distros.Value[0] = d
	return e
}

// WSL 3.0.5 opens the disk as the user, for reading (microsoft/WSL#42048).
func TestOwnershipUserRead(t *testing.T) {
	r := (Ownership{}).Run(unreadableEnv("3.0.5.0", false))
	if r.Status != probe.Fail || !strings.Contains(r.Detail, "42048") || r.FixHint == "" {
		t.Fatalf("unreadable on 3.0.5 -> %s %q", r.Status, r.Summary)
	}
	r = (Ownership{}).Run(unreadableEnv("3.0.3.0", false))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "3.0.5") {
		t.Fatalf("unreadable on 3.0.3 -> %s %q", r.Status, r.Summary)
	}
	if r := (Ownership{}).Run(unreadableEnv("3.0.5.0", true)); r.Status != probe.OK {
		t.Fatalf("readable, admin-owned on 3.0.5 -> %s %q", r.Status, r.Summary)
	}
}
