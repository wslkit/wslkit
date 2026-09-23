//go:build windows

// Package wslcsess finds wslc sessions and says whether each one's VM is
// running, without asking wslc, which would boot a stopped one.
//
// A session is a directory under %LOCALAPPDATA%\wslc\sessions, named for the
// session. While its VM runs, the VM's disk (storage.vhdx) is attached and the
// Restart Manager reports it held by System; while it is stopped, nobody holds
// it. Measured on WSL 2.9.12 across several boots and idle stops, unelevated.
// The Restart Manager holds no handle on the file, so asking cannot get in the
// way of a VM that is starting. See docs/research/2026-09-wslc-session.md.
package wslcsess

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/wslkit/wslkit/internal/winapi/rstrtmgr"
)

// Session is one wslc session.
type Session struct {
	Name    string
	Running bool
	// Err says why it could not be told whether the VM is running. Such a
	// session must be treated as stopped: guessing "running" would boot it.
	Err error
}

// Root is where wslc keeps its sessions for the signed-in user.
func Root() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "wslc", "sessions")
}

// List is ListIn(Root()).
func List() ([]Session, error) { return ListIn(Root()) }

// ListIn lists the sessions under root. A root that does not exist is no
// sessions, not an error: wslc makes it with its first session.
func ListIn(root string) ([]Session, error) {
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		s := Session{Name: d.Name()}
		held, err := rstrtmgr.Holders(filepath.Join(root, d.Name(), "storage.vhdx"))
		if err != nil {
			s.Err = err
		} else {
			s.Running = len(held) > 0
		}
		out = append(out, s)
	}
	return out, nil
}
