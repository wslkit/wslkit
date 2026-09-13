package disk

import (
	"errors"
	"strings"
	"testing"
)

func reg(name string, opts ...func(*Registration)) Registration {
	r := Registration{
		GUID:     "{" + name + "}",
		Name:     name,
		Version:  2,
		State:    StateNormal,
		BasePath: `C:\wsl\` + name,
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func TestVhdPathDefaultsTheFileNameTheWayWSLDoes(t *testing.T) {
	r := reg("Ubuntu")
	if got, want := r.VhdPath(), `C:\wsl\Ubuntu\ext4.vhdx`; got != want {
		t.Errorf("default name: got %q want %q", got, want)
	}
	r.VhdFileName = "custom.vhdx"
	if got, want := r.VhdPath(), `C:\wsl\Ubuntu\custom.vhdx`; got != want {
		t.Errorf("explicit name: got %q want %q", got, want)
	}
}

// An extended-length prefix is kept in BasePath because some registrations have
// it and some do not, and rewriting one is an unrequested change to a value the
// tool does not own. It must still be stripped when building a path to use.
func TestVhdPathStripsTheExtendedLengthPrefix(t *testing.T) {
	r := reg("Ubuntu", func(r *Registration) { r.BasePath = `\\?\C:\wsl\Ubuntu` })
	if got, want := r.VhdPath(), `C:\wsl\Ubuntu\ext4.vhdx`; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if r.BasePath != `\\?\C:\wsl\Ubuntu` {
		t.Error("BasePath itself must not be rewritten")
	}
}

func TestVhdPathIsEmptyForWSL1AndForAMissingBasePath(t *testing.T) {
	if p := reg("One", func(r *Registration) { r.Version = 1 }).VhdPath(); p != "" {
		t.Errorf("WSL 1 should have no disk path, got %q", p)
	}
	if p := reg("Two", func(r *Registration) { r.BasePath = "" }).VhdPath(); p != "" {
		t.Errorf("a registration with no base path should have no disk path, got %q", p)
	}
}

func TestResolveByName(t *testing.T) {
	list := []Registration{
		reg("Ubuntu", func(r *Registration) { r.IsDefault = true }),
		reg("Debian"),
		reg("docker-desktop"),
	}
	for _, c := range []struct{ in, want string }{
		{"Debian", "Debian"},
		{"debian", "Debian"},
		{"DOCKER-DESKTOP", "docker-desktop"},
		{"", "Ubuntu"},
	} {
		got, err := Resolve(list, c.in)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", c.in, err)
		}
		if got.Name != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.in, got.Name, c.want)
		}
	}
}

// wsl.exe matches names case-insensitively, so wslkit does too; but a user with
// two distributions differing only in case must still be able to address each,
// so an exact match wins.
func TestResolvePrefersAnExactMatchOverACaseInsensitiveOne(t *testing.T) {
	list := []Registration{reg("ubuntu"), reg("Ubuntu")}
	got, err := Resolve(list, "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ubuntu" {
		t.Errorf("got %q, want the exact match", got.Name)
	}
}

func TestResolveRefusesToGuessBetweenTwoCaseInsensitiveMatches(t *testing.T) {
	list := []Registration{reg("ubuntu"), reg("UBUNTU")}
	_, err := Resolve(list, "Ubuntu")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := Resolve(nil, "Ubuntu"); !errors.Is(err, ErrNoDistros) {
		t.Errorf("empty list: want ErrNoDistros, got %v", err)
	}
	if _, err := Resolve([]Registration{reg("Ubuntu")}, ""); !errors.Is(err, ErrNoDefault) {
		t.Errorf("no default: want ErrNoDefault, got %v", err)
	}
	_, err := Resolve([]Registration{reg("Ubuntu")}, "Nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	// The message must name what is available, or the user has to run a
	// second command to find out what they should have typed.
	if !strings.Contains(err.Error(), "Ubuntu") {
		t.Errorf("the error should list the known distributions: %v", err)
	}
}

func TestSortedPutsTheDefaultFirstThenAlphabetical(t *testing.T) {
	list := []Registration{reg("zeta"), reg("Alpha"), reg("mid", func(r *Registration) { r.IsDefault = true })}
	got := Names(list)
	want := []string{"mid", "Alpha", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// Sorted must not reorder the caller's slice.
	if list[0].Name != "zeta" {
		t.Error("Sorted mutated its input")
	}
}

func TestCheckPassesForAHealthyStoppedDistro(t *testing.T) {
	ps := Check(State{Reg: reg("Ubuntu"), VhdExists: true})
	if u := Unmet(ps); len(u) != 0 {
		t.Fatalf("unexpected failures: %+v", u)
	}
	if err := FirstError(ps); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

// Every failing precondition is reported, not just the first, so a user fixes
// one thing and succeeds rather than meeting the next obstacle on the next run.
func TestCheckReportsEveryFailureAtOnce(t *testing.T) {
	ps := Check(State{
		Reg:     reg("Old", func(r *Registration) { r.Version = 1; r.State = StateInstalling }),
		Running: true,
	})
	unmet := Unmet(ps)
	names := map[string]bool{}
	for _, p := range unmet {
		names[p.Name] = true
		if p.Detail == "" {
			t.Errorf("precondition %q failed with no explanation", p.Name)
		}
	}
	for _, want := range []string{"wsl2", "state_normal", "vhdx", "vhd_exists", "stopped"} {
		if !names[want] {
			t.Errorf("expected %q to fail; got %v", want, names)
		}
	}
}

func TestCheckMapsFailuresToSentinelErrors(t *testing.T) {
	wsl1 := Check(State{Reg: reg("One", func(r *Registration) { r.Version = 1 }), VhdExists: true})
	if err := FirstError(wsl1); !errors.Is(err, ErrNotWSL2) {
		t.Errorf("want ErrNotWSL2, got %v", err)
	}
	running := Check(State{Reg: reg("Up"), VhdExists: true, Running: true})
	if err := FirstError(running); !errors.Is(err, ErrRunning) {
		t.Errorf("want ErrRunning, got %v", err)
	}
}

// A distribution that is stopped but whose disk is still held gets a different
// explanation from one that is simply running, because the remedy differs.
func TestCheckDistinguishesRunningFromADiskHeldOpen(t *testing.T) {
	ps := Check(State{Reg: reg("Ubuntu"), VhdExists: true, VhdInUse: true})
	unmet := Unmet(ps)
	if len(unmet) != 1 || unmet[0].Name != "disk_free" {
		t.Fatalf("expected only disk_free to fail, got %+v", unmet)
	}
	if !strings.Contains(unmet[0].Detail, "another process") {
		t.Errorf("the detail should point at the other process: %q", unmet[0].Detail)
	}
}

func TestDecodeFlags(t *testing.T) {
	for _, c := range []struct {
		in   int
		want string
	}{
		{0, "none"},
		{1, "interop"},
		{7, "interop, append-nt-path, drive-mounting"},
		{15, "interop, append-nt-path, drive-mounting, undocumented(0x8)"},
		{8, "undocumented(0x8)"},
	} {
		if got := DecodeFlags(c.in); got != c.want {
			t.Errorf("DecodeFlags(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
