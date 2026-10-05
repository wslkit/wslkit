package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// healthy is Ubuntu on WSL 3.0.1 as measured, with a shell of the default
// user open, minus the systemd-binfmt failure.
func healthy() env.SystemdState {
	return env.SystemdState{
		PID1: "systemd", System: "running", UID: 1000, UserKnown: true,
		UserService: "active", RunUserExists: true, RunUserOwner: 1000, RunUserMode: "700",
		PAMSystemd: true, UserProcs: 4, UserProcsXDG: 3, XDG: "/run/user/1000",
	}
}

func sysEnv(s env.SystemdState, wslconf string) *env.Env {
	e := envWithWslConf(wslconf)
	list := e.DistroList()
	list[0].Systemd = env.Ok(s, "t")
	e.Distros = env.Ok(list, "t")
	return e
}

func TestSystemdHealthy(t *testing.T) {
	r := (Systemd{}).Run(sysEnv(healthy(), "[boot]\nsystemd=true\n"))
	if r.Status != probe.OK {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}

// Each fault of the family in microsoft/WSL#13826 has its own finding.
func TestSystemdFindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wslconf string
		mutate  func(*env.SystemdState)
		want    string
	}{
		{"asked for but not PID 1", "[boot]\nsystemd=true\n",
			func(s *env.SystemdState) { *s = env.SystemdState{PID1: "init", UID: 1000} }, "boot.systemd=true, but PID 1 is init"},
		{"degraded", "", func(s *env.SystemdState) {
			s.System, s.Failed = "degraded", []string{"systemd-binfmt.service", "snapd.service"}
		}, "degraded, 1 failed unit(s)"},
		{"maintenance", "", func(s *env.SystemdState) { s.System = "maintenance" }, `systemd reports "maintenance"`},
		{"user session missing (the 31-vote case)", "", func(s *env.SystemdState) {
			s.UserService, s.RunUserExists, s.UserProcsXDG, s.XDG = "inactive", false, 0, ""
		}, "user session for uid 1000 is not running"},
		{"uid not in passwd", "", func(s *env.SystemdState) { s.UserKnown = false }, "default uid 1000 is not in /etc/passwd"},
		{"run dir missing", "", func(s *env.SystemdState) { s.RunUserExists = false }, "/run/user/1000 does not exist"},
		{"run dir wrong owner", "", func(s *env.SystemdState) { s.RunUserOwner, s.RunUserMode = 0, "755" }, "/run/user/1000 is owned by uid 0 with mode 755"},
		{"XDG wrong", "", func(s *env.SystemdState) { s.XDG = "/tmp" }, "XDG_RUNTIME_DIR is /tmp, not /run/user/1000"},
		{"XDG unset", "", func(s *env.SystemdState) { s.UserProcsXDG, s.XDG = 0, "" }, "XDG_RUNTIME_DIR is not set"},
	} {
		s := healthy()
		tc.mutate(&s)
		r := (Systemd{}).Run(sysEnv(s, tc.wslconf))
		if r.Status != probe.Warn || !strings.Contains(r.Summary, tc.want) {
			t.Errorf("%s: %s %q, want %q", tc.name, r.Status, r.Summary, tc.want)
		}
	}
}

// A missing user session names pam_systemd when no PAM file loads it.
func TestSystemdNamesMissingPAM(t *testing.T) {
	s := healthy()
	s.UserService, s.PAMSystemd = "inactive", false
	if r := (Systemd{}).Run(sysEnv(s, "")); !strings.Contains(r.Detail, "pam_systemd") {
		t.Errorf("detail %q", r.Detail)
	}
}

// Not faults: systemd simply off, degraded only by a unit that fails
// harmlessly under WSL, no user process yet, and a root default user.
func TestSystemdNonFindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*env.SystemdState)
	}{
		{"systemd off", func(s *env.SystemdState) { *s = env.SystemdState{PID1: "init", UID: 1000} }},
		{"only systemd-binfmt failed", func(s *env.SystemdState) {
			s.System, s.Failed = "degraded", []string{"systemd-binfmt.service"}
		}},
		{"no user process yet", func(s *env.SystemdState) {
			s.UserService, s.RunUserExists, s.UserProcs, s.UserProcsXDG, s.XDG = "inactive", false, 0, 0, ""
		}},
		{"root default user", func(s *env.SystemdState) {
			s.UID, s.UserService, s.RunUserExists, s.UserProcsXDG = 0, "inactive", false, 0
		}},
	} {
		s := healthy()
		tc.mutate(&s)
		if r := (Systemd{}).Run(sysEnv(s, "")); r.Status != probe.OK {
			t.Errorf("%s: %s %q", tc.name, r.Status, r.Summary)
		}
	}
}

// Stopped is not asked: starting the distribution would start systemd.
func TestSystemdSkipsAStoppedDistribution(t *testing.T) {
	e := envWithWslConf("")
	list := e.DistroList()
	list[0].Systemd = env.Fail[env.SystemdState](env.ErrVMWakeRefused, "t", errString("stopped"))
	e.Distros = env.Ok(list, "t")
	r := (Systemd{}).Run(e)
	if r.Status != probe.Skipped || !strings.Contains(r.Summary, "not running") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}

// A snapshot from before this check has no systemd field at all. It must
// come out skipped, not as a clean bill of health.
func TestSystemdOnASnapshotThatNeverCollectedIt(t *testing.T) {
	r := (Systemd{}).Run(envWithWslConf(""))
	if r.Status != probe.Skipped {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}
