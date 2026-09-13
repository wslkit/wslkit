package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// envWithZone describes a machine with one running distribution whose scan
// found the given files.
func envWithZone(scan env.ZoneScan) *env.Env {
	e := env.New("t")
	d := env.Distro{GUID: "{a}", Name: "Ubuntu", Version: 2, IsDefault: true}
	d.Running = env.Ok(true, "t")
	d.ZoneFiles = env.Ok(scan, "t")
	e.Distros = env.Ok([]env.Distro{d}, "t")
	return e
}

func TestZoneClean(t *testing.T) {
	r := (ZoneFiles{}).Run(envWithZone(env.ZoneScan{}))
	if r.Status != probe.OK {
		t.Fatalf("an empty scan should pass: %s %q", r.Status, r.Summary)
	}
}

func TestZoneReportsCountAndExamples(t *testing.T) {
	r := (ZoneFiles{}).Run(envWithZone(env.ZoneScan{
		Count: 3,
		Paths: []string{"/home/ana/Downloads/a.exe:Zone.Identifier", "/home/ana/b.pdf:Zone.Identifier", "/root/c.iso:Zone.Identifier"},
	}))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "Ubuntu: 3 Zone.Identifier") {
		t.Errorf("the summary should name the distribution and the count: %q", r.Summary)
	}
	if !strings.Contains(r.Detail, "/home/ana/Downloads/a.exe:Zone.Identifier") {
		t.Errorf("the detail should show an example: %q", r.Detail)
	}
	if !strings.Contains(r.FixHint, "fix zone") {
		t.Errorf("the hint should point at the fix: %q", r.FixHint)
	}
}

// A walk that gave up early knows it found a floor, not a total, and saying
// "3" when the real number is larger would send someone away satisfied.
func TestZoneTruncatedCountIsAFloor(t *testing.T) {
	r := (ZoneFiles{}).Run(envWithZone(env.ZoneScan{Count: 3, Truncated: true, Paths: []string{"/home/a:Zone.Identifier"}}))
	if !strings.Contains(r.Summary, "at least 3") {
		t.Errorf("a truncated count should be reported as a floor: %q", r.Summary)
	}
}

// A clean result from a walk that stopped early is worth less than a clean
// result from one that finished, and the confidence has to say so.
func TestZoneTruncatedCleanIsLessConfident(t *testing.T) {
	r := (ZoneFiles{}).Run(envWithZone(env.ZoneScan{Truncated: true}))
	if r.Status != probe.OK {
		t.Fatalf("status %s", r.Status)
	}
	if r.Confidence >= 1 {
		t.Errorf("confidence = %v, want less than certain", r.Confidence)
	}
}

// A stopped distribution is skipped, not passed: reading it would start it,
// and reporting "no files" for a tree nobody looked at would be a lie.
func TestZoneStoppedDistroIsSkipped(t *testing.T) {
	e := envWithZone(env.ZoneScan{})
	list := e.DistroList()
	list[0].Running = env.Ok(false, "t")
	list[0].ZoneFiles = env.Fail[env.ZoneScan](env.ErrVMWakeRefused, "p", errStopped)
	e.Distros = env.Ok(list, "t")

	r := (ZoneFiles{}).Run(e)
	if r.Status != probe.Skipped {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "not running") {
		t.Errorf("the summary should say why: %q", r.Summary)
	}
}

func TestZoneNoDistros(t *testing.T) {
	e := env.New("t")
	e.Distros = env.Ok([]env.Distro{}, "t")
	if r := (ZoneFiles{}).Run(e); r.Status != probe.OK {
		t.Fatalf("status %s", r.Status)
	}
}

var errStopped = errStr("the distribution is stopped")

type errStr string

func (e errStr) Error() string { return string(e) }
