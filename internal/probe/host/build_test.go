package host

import (
	"strings"
	"testing"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
)

func osEnv(build int, pending bool) *env.Env {
	e := env.New("t")
	e.Host.OS = env.Ok(env.OSBuild{Major: 10, Build: build, DisplayVersion: "x"}, "t")
	e.Host.PendingReboot = env.Ok(pending, "t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	e.Distros = env.Ok([]env.Distro{{Name: "u", Version: 2}}, "t")
	return e
}

func TestBuild(t *testing.T) {
	if r := (Build{}).Run(osEnv(18363, false)); r.Status != probe.Fail || !strings.Contains(r.Summary, "19041") {
		t.Fatalf("old build -> %s %q", r.Status, r.Summary)
	}
	if r := (Build{}).Run(osEnv(19045, false)); r.Status != probe.OK || !strings.Contains(r.Detail, "2025-10-14") {
		t.Fatalf("win10 -> %s %q", r.Status, r.Detail)
	}
	r := (Build{}).Run(osEnv(26100, true))
	if r.Status != probe.Warn || r.Confidence != 0.45 || r.FixHint == "" {
		t.Fatalf("pending reboot -> %s %.2f", r.Status, r.Confidence)
	}
	e := osEnv(26100, true)
	e.Distros = env.Ok([]env.Distro{}, "t")
	if r := (Build{}).Run(e); r.Confidence != 0.6 || !strings.Contains(r.Summary, "refuses to install") {
		t.Fatalf("pending + no distros -> %.2f %q", r.Confidence, r.Summary)
	}
	e = env.New("t")
	if r := (Build{}).Run(e); r.Status != probe.Unknown {
		t.Fatalf("no OS -> %s", r.Status)
	}
}

func TestCOMClass(t *testing.T) {
	e := osEnv(19045, false)
	if r := (COMClass{}).Run(e); r.Status != probe.Skipped || !strings.Contains(r.Summary, "not collected") {
		t.Fatalf("uncollected -> %s %q", r.Status, r.Summary)
	}
	e.Runtime.COMClassRegistered = env.Ok(true, "HKCR")
	if r := (COMClass{}).Run(e); r.Status != probe.OK {
		t.Fatalf("registered -> %s", r.Status)
	}
	e.Runtime.COMClassRegistered = env.Ok(false, "HKCR")
	r := (COMClass{}).Run(e)
	if r.Status != probe.Fail || r.FixID != "update" || !strings.Contains(r.Detail, "0x80040154") {
		t.Fatalf("missing -> %s %q", r.Status, r.Detail)
	}
	e.Runtime.Version = env.Absent[string]("p")
	if r := (COMClass{}).Run(e); r.Status != probe.Skipped {
		t.Fatalf("no runtime -> %s", r.Status)
	}
}
