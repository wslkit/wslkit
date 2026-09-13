package actions

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
	"github.com/wslkit/wslkit/internal/wslpath"
)

func zoneEnv(scan env.ZoneScan) *env.Env {
	e := env.New("t")
	d := env.Distro{GUID: "{a}", Name: "Ubuntu", Version: 2, IsDefault: true}
	d.Running = env.Ok(true, "t")
	d.ZoneFiles = env.Ok(scan, "t")
	e.Distros = env.Ok([]env.Distro{d}, "t")
	return e
}

func TestZoneFixDeletesEachRecordedPath(t *testing.T) {
	e := zoneEnv(env.ZoneScan{Count: 2, Paths: []string{
		"/home/ana/Downloads/a.exe:Zone.Identifier",
		"/root/b.iso:Zone.Identifier",
	}})
	p, err := (Zone{}).Plan(e, fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(p.Steps))
	}
	// The path has to be the Windows one that actually reaches the file: back
	// slashes on every platform the tests run on, and the colon written the
	// way WSL stores it rather than as a colon, which Windows would read as
	// the start of an alternate data stream.
	want := wslpath.UNC("Ubuntu", "/home/ana/Downloads/a.exe:Zone.Identifier")
	if !strings.Contains(want, `\\wsl.localhost\Ubuntu\home\ana\Downloads\a.exe`) || strings.HasSuffix(want, ":Zone.Identifier") {
		t.Fatalf("the expected path is not what it should be: %q", want)
	}
	if p.Steps[0].Kind != "file_delete" || p.Steps[0].Args[0] != want {
		t.Errorf("step 0 = %s %v", p.Steps[0].Kind, p.Steps[0].Args)
	}
}

// Deleting is not reversible, and a plan that offered a rollback would be
// promising something it cannot do.
func TestZoneFixSaysItCannotBeUndone(t *testing.T) {
	p, err := (Zone{}).Plan(zoneEnv(env.ZoneScan{Count: 1, Paths: []string{"/home/a:Zone.Identifier"}}), fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rollback) != 1 || p.Rollback[0].Kind != "note" {
		t.Fatalf("rollback = %+v, want a single note", p.Rollback)
	}
	if !strings.Contains(strings.Join(warnText(p), " "), "cannot be undone") {
		t.Errorf("warnings should say so: %v", p.Warnings)
	}
}

func TestZoneFixPathFilter(t *testing.T) {
	e := zoneEnv(env.ZoneScan{Count: 3, Paths: []string{
		"/home/ana/a.exe:Zone.Identifier",
		"/home/bob/b.exe:Zone.Identifier",
		"/root/c.iso:Zone.Identifier",
	}})
	p, err := (Zone{}).Plan(e, fix.Options{Args: []string{"--path", "/home/ana"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || !strings.Contains(p.Steps[0].Args[0], `ana\a.exe`) {
		t.Fatalf("steps = %+v", p.Steps)
	}
}

func TestZoneFixUnknownDistroIsAnError(t *testing.T) {
	_, err := (Zone{}).Plan(zoneEnv(env.ZoneScan{}), fix.Options{Args: []string{"--distro", "Debian"}})
	if err == nil {
		t.Fatal("naming a distribution that is not there should fail, not silently do nothing")
	}
}

func TestZoneFixRejectsARelativePath(t *testing.T) {
	if _, err := (Zone{}).Plan(zoneEnv(env.ZoneScan{}), fix.Options{Args: []string{"--path", "home/ana"}}); err == nil {
		t.Fatal("--path is a path inside the distribution and has to be absolute")
	}
}

// Nothing to do is a plan that says so, not an error: running the fix when the
// machine is already clean should be unremarkable.
func TestZoneFixNothingToDo(t *testing.T) {
	p, err := (Zone{}).Plan(zoneEnv(env.ZoneScan{}), fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Kind != "note" {
		t.Fatalf("steps = %+v", p.Steps)
	}
}

// A stopped distribution has not been scanned, so the fix has to say that
// rather than report a clean machine.
func TestZoneFixSaysWhenNothingWasScanned(t *testing.T) {
	e := zoneEnv(env.ZoneScan{})
	list := e.DistroList()
	list[0].ZoneFiles = env.Fail[env.ZoneScan](env.ErrVMWakeRefused, "p", errStopped)
	e.Distros = env.Ok(list, "t")

	p, err := (Zone{}).Plan(e, fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Steps[0].Description, "not running") {
		t.Errorf("description = %q", p.Steps[0].Description)
	}
}

// Past the path cap the scan knows there are more than it listed, and a fix
// that deleted its list and reported success would look like it had finished.
func TestZoneFixWarnsWhenTheListIsShortOfTheCount(t *testing.T) {
	p, err := (Zone{}).Plan(zoneEnv(env.ZoneScan{Count: 10, Paths: []string{"/home/a:Zone.Identifier"}}), fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(warnText(p), " "), "run the fix again") {
		t.Errorf("warnings = %v", p.Warnings)
	}
}

func warnText(p fix.Plan) []string { return p.Warnings }

var errStopped = errStr("the distribution is stopped")

type errStr string

func (e errStr) Error() string { return string(e) }
