package config

import (
	"path/filepath"
	"testing"
)

func TestParseTarget(t *testing.T) {
	good := map[string]string{
		`npipe:\\.\pipe\openssh-ssh-agent`:                    "npipe",
		`NPIPE:\\.\PIPE\x`:                                    "npipe",
		"tcp:127.0.0.1:3128":                                  "tcp",
		`assuan:C:\Users\u\AppData\Roaming\gnupg\S.gpg-agent`: "assuan",
		"unix:/run/wslkit/ssh-agent.sock":                     "unix",
	}
	for in, want := range good {
		s, _, err := ParseTarget(in)
		if err != nil || s != want {
			t.Errorf("%q -> %q %v", in, s, err)
		}
	}
	for _, bad := range []string{"", "npipe", "npipe:", `npipe:C:\x`, "tcp:nohostport", "ftp:x", ":x", "unix:relative"} {
		if _, _, err := ParseTarget(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHostAllowAndFilters(t *testing.T) {
	h := Host{Port: 1, Allow: []string{`npipe:\\.\pipe\openssh-ssh-agent`}, Filters: map[string]string{`npipe:\\.\pipe\openssh-ssh-agent`: "ssh-agent"}}
	if !h.Allowed(`NPIPE:\\.\pipe\OPENSSH-SSH-AGENT`) || h.Allowed(`npipe:\\.\pipe\notlisted`) {
		t.Fatal("allow list")
	}
	if h.FilterFor(`npipe:\\.\pipe\openssh-ssh-agent`) != "ssh-agent" || h.FilterFor("x") != "" {
		t.Fatal("filters")
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Host{Port: 1, Allow: []string{"bogus"}}).Validate(); err == nil {
		t.Fatal("bad allow entry accepted")
	}
	if err := (Host{}).Validate(); err == nil {
		t.Fatal("port 0 accepted")
	}
}

func TestGuestValidate(t *testing.T) {
	g := Guest{Port: DefaultPort, Listeners: []Listener{{Name: "ssh-agent", Unix: "/run/wslkit/ssh-agent.sock", Target: `npipe:\\.\pipe\openssh-ssh-agent`, OwnerUID: 1000, Mode: 0o600}}}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	g.Listeners = append(g.Listeners, g.Listeners[0])
	if err := g.Validate(); err == nil {
		t.Fatal("duplicate socket accepted")
	}
	g.Listeners = []Listener{{Name: "x", Unix: "relative", Target: "unix:/x"}}
	if err := g.Validate(); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	hp := filepath.Join(dir, "host.json")
	h := DefaultHost()
	h.Allow = []string{`npipe:\\.\pipe\openssh-ssh-agent`}
	h.Filters[h.Allow[0]] = "ssh-agent"
	if err := Save(hp, h); err != nil {
		t.Fatal(err)
	}
	got, err := LoadHost(hp)
	if err != nil || len(got.Allow) != 1 || got.FilterFor(h.Allow[0]) != "ssh-agent" || got.Port != DefaultPort {
		t.Fatalf("%+v %v", got, err)
	}
	missing, err := LoadHost(filepath.Join(dir, "none.json"))
	if err != nil || missing.Port != DefaultPort {
		t.Fatalf("missing file should yield defaults: %+v %v", missing, err)
	}
	gp := filepath.Join(dir, "guest.json")
	g := DefaultGuest()
	g.Listeners = []Listener{{Name: "a", Unix: "/run/a.sock", Target: "npipe:\\\\.\\pipe\\a", OwnerUID: -1}}
	if err := Save(gp, g); err != nil {
		t.Fatal(err)
	}
	if lg, err := LoadGuest(gp); err != nil || len(lg.Listeners) != 1 {
		t.Fatalf("%+v %v", lg, err)
	}
}
