//go:build windows

package top

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/wslkit/wslkit/internal/winapi/wslcsess"
)

// wslcTimeout bounds the wslc calls that are not a measurement.
const wslcTimeout = 30 * time.Second

// wslc finds wslc.exe: on PATH, or where the WSL package installs it.
func wslc() (string, error) {
	if p, err := exec.LookPath("wslc.exe"); err == nil {
		return p, nil
	}
	p := filepath.Join(os.Getenv("ProgramFiles"), "WSL", "wslc.exe")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", errors.New("wslc is not installed; it ships with the WSL 2.9 pre-releases")
}

// Sessions lists the wslc sessions whose VM is already running. It never asks
// wslc, which would boot a stopped one: wslcsess tells from whether the VM's
// disk is attached. A session whose state cannot be told is left out, since
// guessing "running" would start it. Without wslc there are none.
func (r WSLRunner) Sessions(ctx context.Context) ([]string, error) {
	if _, err := wslc(); err != nil {
		return nil, nil
	}
	sessions, err := wslcsess.List()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, s := range sessions {
		if s.Running && s.Err == nil {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// SampleSession runs SessionScript inside the session's VM, on standard input
// for the same reason Sample does. This boots the VM if it was stopped.
func (r WSLRunner) SampleSession(ctx context.Context, name string, timeout time.Duration) (string, error) {
	exe, err := wslc()
	if err != nil {
		return "", err
	}
	out, stderr, err := WSLRunner{Exe: exe}.run(ctx, timeout, []byte(SessionScript), "--session", name, "system", "session", "run", "/bin/sh")
	if err != nil {
		return out, fmt.Errorf("top: measuring wslc session %s: %w: %s", name, err, oneLine(stderr))
	}
	return out, nil
}

// ContainerNames is the session's running containers, one JSON object a line.
func (r WSLRunner) ContainerNames(ctx context.Context, name string) (string, error) {
	exe, err := wslc()
	if err != nil {
		return "", err
	}
	out, stderr, err := WSLRunner{Exe: exe}.run(ctx, wslcTimeout, nil, "--session", name, "list", "--format", "json")
	if err != nil {
		return out, fmt.Errorf("top: wslc list: %w: %s", err, oneLine(stderr))
	}
	return out, nil
}

// SessionDisk is the size of a session's storage.vhdx on Windows.
func (r WSLRunner) SessionDisk(name string) (uint64, bool) {
	st, err := os.Stat(filepath.Join(wslcsess.Root(), name, "storage.vhdx"))
	if err != nil {
		return 0, false
	}
	return uint64(st.Size()), true
}
