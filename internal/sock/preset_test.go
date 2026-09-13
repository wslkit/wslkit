package sock

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/agent/config"
)

func TestLookupAndNames(t *testing.T) {
	if _, ok := Lookup("SSH-AGENT"); !ok {
		t.Fatal("case-insensitive lookup")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown preset accepted")
	}
	names := Names()
	if len(names) != len(All) || names[0] != "ssh-agent" {
		t.Fatalf("names = %v", names)
	}
}

func TestExpand(t *testing.T) {
	if got := Expand("%r/wslkit/x.sock", 1000, "/run/user/1000"); got != "/run/user/1000/wslkit/x.sock" {
		t.Fatal(got)
	}
	if got := Expand("%r/x", 1001, ""); got != "/run/user/1001/x" {
		t.Fatal(got)
	}
	if got := Expand("/tmp/%u.sock", 42, ""); got != "/tmp/42.sock" {
		t.Fatal(got)
	}
}

func TestListenerAndEnv(t *testing.T) {
	p, _ := Lookup("ssh-agent")
	l := p.Listener(p.Target, 1000, "/run/user/1000")
	if l.Unix != "/run/user/1000/wslkit/ssh-agent.sock" || l.OwnerUID != 1000 || l.Mode != 0o600 || l.Filter != "ssh-agent" {
		t.Fatalf("%+v", l)
	}
	if err := (config.Guest{Port: 1, Listeners: []config.Listener{l}}).Validate(); err != nil {
		t.Fatal(err)
	}
	env := p.EnvLines(1000, "/run/user/1000")
	if len(env) != 1 || !strings.HasPrefix(env[0], "export SSH_AUTH_SOCK=/run/user/1000/") {
		t.Fatalf("%v", env)
	}
	// A preset with no env yields no lines.
	extra, _ := Lookup("gpg-agent-extra")
	if len(extra.EnvLines(1000, "")) != 0 {
		t.Fatal("unexpected env")
	}
}

func TestAllPresetsAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All {
		if p.Name == "" || p.Desc == "" || p.Unix == "" {
			t.Errorf("%q incomplete", p.Name)
		}
		if seen[p.Name] {
			t.Errorf("duplicate preset %q", p.Name)
		}
		seen[p.Name] = true
		if p.Target != "" {
			if _, _, err := config.ParseTarget(p.Target); err != nil {
				t.Errorf("%s: %v", p.Name, err)
			}
		}
		if !strings.HasPrefix(p.Unix, "%r") && !strings.HasPrefix(p.Unix, "/") {
			t.Errorf("%s: unix path must be absolute or start with %%r", p.Name)
		}
	}
}
