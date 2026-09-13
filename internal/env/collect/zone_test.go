package collect

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWalkZoneRootFindsStreamFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"ana/Downloads/installer.exe":                  {Data: []byte("x")},
		"ana/Downloads/installer.exe:Zone.Identifier":  {Data: []byte("[ZoneTransfer]")},
		"ana/notes.txt":                                {Data: []byte("x")},
		"ana/work/report.pdf:Zone.Identifier":          {Data: []byte("[ZoneTransfer]")},
		"ana/work/node_modules/a/b.js:Zone.Identifier": {Data: []byte("[ZoneTransfer]")},
	}
	w := &zoneWalk{}
	walkZoneRoot(context.Background(), fsys, "home", w)
	got := w.result()

	// The one under node_modules is deliberately not counted: nobody saves a
	// download in there, and walking it is what makes the scan slow.
	if got.Count != 2 {
		t.Fatalf("count = %d, want 2 (paths %v)", got.Count, got.Paths)
	}
	if got.Truncated {
		t.Errorf("truncated, but nothing hit a limit")
	}
	want := []string{"/home/ana/Downloads/installer.exe:Zone.Identifier", "/home/ana/work/report.pdf:Zone.Identifier"}
	for _, p := range want {
		if !contains(got.Paths, p) {
			t.Errorf("missing %q in %v", p, got.Paths)
		}
	}
}

func TestWalkZoneRootStopsAtDepth(t *testing.T) {
	deep := "a/b/c/d/e/f/g/h/buried.txt:Zone.Identifier"
	fsys := fstest.MapFS{
		"shallow.txt:Zone.Identifier": {Data: []byte("x")},
		deep:                          {Data: []byte("x")},
	}
	w := &zoneWalk{}
	walkZoneRoot(context.Background(), fsys, "root", w)
	got := w.result()
	if got.Count != 1 {
		t.Fatalf("count = %d, want 1 (paths %v)", got.Count, got.Paths)
	}
	if got.Paths[0] != "/root/shallow.txt:Zone.Identifier" {
		t.Errorf("path = %q", got.Paths[0])
	}
}

// A cancelled context must not be reported as a clean, empty result: the caller
// turns the walk's own truncation flag into "at least N".
func TestWalkZoneRootStopsOnCancel(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := 0; i < 200; i++ {
		fsys[fmt.Sprintf("f%03d.bin:Zone.Identifier", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &zoneWalk{}
	walkZoneRoot(ctx, fsys, "home", w)
	if got := w.result(); got.Count != 0 {
		t.Errorf("count = %d, want 0 from an already-cancelled walk", got.Count)
	}
}

// Past the path cap the count must keep climbing even though the list stops,
// because that gap is what tells the fix there is more to do.
func TestWalkZoneRootCapsPathsNotCount(t *testing.T) {
	fsys := fstest.MapFS{}
	const n = zoneMaxPaths + 7
	for i := 0; i < n; i++ {
		fsys[fmt.Sprintf("d/f%04d.bin:Zone.Identifier", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	w := &zoneWalk{}
	walkZoneRoot(context.Background(), fsys, "home", w)
	got := w.result()
	if got.Count != n {
		t.Errorf("count = %d, want %d", got.Count, n)
	}
	if len(got.Paths) != zoneMaxPaths {
		t.Errorf("paths = %d, want %d", len(got.Paths), zoneMaxPaths)
	}
}

func TestWalkZoneRootIgnoresOrdinaryFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"a/zone.identifier":           {Data: []byte("x")},
		"a/notes:Zone.Identifier.bak": {Data: []byte("x")},
		"a/real.iso:Zone.Identifier":  {Data: []byte("x")},
		"a/Zone.Identifier":           {Data: []byte("x")},
	}
	w := &zoneWalk{}
	walkZoneRoot(context.Background(), fsys, "home", w)
	got := w.result()
	// The suffix is matched exactly and case-sensitively, because that is
	// what the file server writes. A file merely named similarly belongs to
	// the user.
	if got.Count != 1 || !strings.HasSuffix(got.Paths[0], "real.iso:Zone.Identifier") {
		t.Fatalf("count = %d, paths = %v", got.Count, got.Paths)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
