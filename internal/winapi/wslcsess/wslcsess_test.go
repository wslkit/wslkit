//go:build windows

package wslcsess

import (
	"os"
	"path/filepath"
	"testing"
)

// The same test the product runs, on a sessions directory whose state is known:
// one session's disk is held open, as a running VM's is, and the other's is
// not.
func TestListInTellsRunningFromStopped(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"up", "down"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "storage.vhdx"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.Open(filepath.Join(root, "up", "storage.vhdx"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	got, err := ListIn(root)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]bool{}
	for _, s := range got {
		if s.Err != nil {
			t.Fatalf("%s: %v", s.Name, s.Err)
		}
		state[s.Name] = s.Running
	}
	if len(state) != 2 || !state["up"] || state["down"] {
		t.Errorf("got %+v, want up running and down stopped", got)
	}
}

// wslc makes its sessions directory with its first session; before that
// there is simply nothing.
func TestListInWithNoSessionsDirectory(t *testing.T) {
	got, err := ListIn(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(got) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}
