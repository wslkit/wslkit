//go:build windows

package sock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/agent/config"
)

func TestDiscoverFixedTarget(t *testing.T) {
	p, _ := Lookup("ssh-agent")
	got, warn, err := Discover(p)
	// A missing pipe is advisory, never an error: the agent may start later.
	if err != nil {
		t.Fatalf("named pipe discovery must not fail: %v", err)
	}
	if got != p.Target {
		t.Fatalf("got %q want %q", got, p.Target)
	}
	if warn != "" && !strings.Contains(warn, "openssh-ssh-agent") {
		t.Fatalf("unhelpful warning: %q", warn)
	}
}

func TestDiscoverAssuanTargets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GNUPGHOME", dir)
	if GnupgHome() != dir {
		t.Fatal("GNUPGHOME ignored")
	}
	p, _ := Lookup("gpg-agent")
	if _, _, err := Discover(p); err == nil {
		t.Fatal("missing socket file should be an error")
	} else if !strings.Contains(err.Error(), "gpg-connect-agent") {
		t.Fatalf("error should say how to fix it: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "S.gpg-agent"), []byte("12345\n0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := Discover(p)
	if err != nil {
		t.Fatal(err)
	}
	scheme, rest, err := config.ParseTarget(got)
	if err != nil || scheme != config.SchemeAssuan || !strings.HasSuffix(rest, "S.gpg-agent") {
		t.Fatalf("%q -> %q %q %v", got, scheme, rest, err)
	}
}

func TestProfileBodyForEnabledPresets(t *testing.T) {
	// writeProfile's rendering is exercised through the same helper the wiring
	// uses, without touching a distro.
	g := config.Guest{Port: config.DefaultPort, Listeners: []config.Listener{
		{Name: "ssh-agent", Unix: "/run/user/1000/wslkit/ssh-agent.sock", Target: `npipe:\\.\pipe\openssh-ssh-agent`, OwnerUID: 1000},
		{Name: "gpg-agent-extra", Unix: "/run/user/1000/gnupg/S.gpg-agent.extra", Target: "assuan:C:\\x", OwnerUID: 1000},
	}}
	body := profileBody(g, GuestUser{UID: 1000, RuntimeDir: "/run/user/1000"})
	if !strings.Contains(body, "export SSH_AUTH_SOCK=/run/user/1000/wslkit/ssh-agent.sock") {
		t.Fatalf("ssh-agent export missing:\n%s", body)
	}
	if strings.Contains(body, "gpg-agent-extra") {
		t.Fatalf("a preset with no env should not appear:\n%s", body)
	}
	if !strings.HasPrefix(body, "# Managed by wslkit") {
		t.Fatalf("missing header:\n%s", body)
	}
	// No presets with env: nothing to write.
	if b := profileBody(config.Guest{Port: 1}, GuestUser{UID: 1000}); b != "" {
		t.Fatalf("expected empty body, got %q", b)
	}
}
