//go:build windows

package rstrtmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// The helper is only as good as its answer for a file whose state is known:
// held open by this very process, then closed.
func TestHoldersSeesThisProcessAndThenNobody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	held, err := Holders(path)
	if err != nil {
		t.Fatal(err)
	}
	var mine bool
	for _, h := range held {
		if h.PID == uint32(os.Getpid()) {
			mine = true
		}
	}
	if !mine {
		t.Errorf("an open file's holders %+v do not include this process (%d)", held, os.Getpid())
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	held, err = Holders(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Errorf("a closed file is still held by %+v", held)
	}
}
