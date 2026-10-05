package collect

import (
	"strings"
	"testing"
)

// Printed by systemdScript(1000) in Ubuntu on WSL 3.0.1, with a shell of the
// default user open. Ubuntu was degraded only by systemd-binfmt.
const ubuntu301 = `pid1=systemd
system=degraded
failed=systemd-binfmt.service
user_service=active
user_known=yes
run_user=1000 700
pam_systemd=yes
user_procs=4
user_procs_xdg=3
xdg=/run/user/1000
`

func TestParseSystemd(t *testing.T) {
	s, err := parseSystemd(strings.ReplaceAll(ubuntu301, "\n", "\r\n"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s.PID1 != "systemd" || s.System != "degraded" || len(s.Failed) != 1 || s.Failed[0] != "systemd-binfmt.service" {
		t.Errorf("system: %+v", s)
	}
	if s.UserService != "active" || !s.UserKnown || !s.RunUserExists || s.RunUserOwner != 1000 || s.RunUserMode != "700" {
		t.Errorf("session: %+v", s)
	}
	if !s.PAMSystemd || s.UserProcs != 4 || s.UserProcsXDG != 3 || s.XDG != "/run/user/1000" || s.UID != 1000 {
		t.Errorf("environment: %+v", s)
	}
}

// A distribution without systemd prints no system lines and no run_user.
func TestParseSystemdWithoutSystemd(t *testing.T) {
	s, err := parseSystemd("pid1=init\nuser_procs=0\nuser_procs_xdg=0\nxdg=\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.PID1 != "init" || s.System != "" || s.RunUserExists || s.UserKnown {
		t.Errorf("%+v", s)
	}
}

func TestParseSystemdRefusesNothing(t *testing.T) {
	if _, err := parseSystemd("wsl: something went wrong\n", 1000); err == nil {
		t.Error("output with no pid1 line must not read as a state")
	}
}

// The uid is the only substitution, and nothing may be left unsubstituted:
// an @UID@ reaching the shell would test against a literal string and find
// nothing, and every check would quietly pass.
func TestSystemdScript(t *testing.T) {
	s := systemdScript(1000)
	if !strings.HasPrefix(s, "uid=1000\n") || strings.Contains(s, "@") && strings.Contains(s, "@UID@") || strings.Contains(s, "@MAX@") {
		t.Errorf("substitution:\n%s", s)
	}
	if strings.Contains(s, "\r") {
		t.Error("a carriage return turns a line into a command that does not exist")
	}
	for _, want := range []string{"systemctl is-system-running", "is-active user@$uid.service", "timeout 3 systemctl", "/run/user/$uid", "XDG_RUNTIME_DIR"} {
		if !strings.Contains(s, want) {
			t.Errorf("script does not contain %q", want)
		}
	}
	// It must not log in: su or login would start the user session it is
	// diagnosing.
	for _, bad := range []string{"su ", "login", "runuser", "machinectl"} {
		if strings.Contains(s, bad) {
			t.Errorf("script must not start a session (%q)", bad)
		}
	}
}
