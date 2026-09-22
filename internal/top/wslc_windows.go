//go:build windows

package top

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

// Sessions lists the sessions wslc knows about. `wslc info` does not start a
// session VM (docs/research/2026-09-wslc-session.md), so this is safe to ask
// even though what follows is not.
func (r WSLRunner) Sessions(ctx context.Context) ([]string, error) {
	exe, err := wslc()
	if err != nil {
		return nil, err
	}
	out, stderr, err := WSLRunner{Exe: exe}.run(ctx, wslcTimeout, nil, "info", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("top: wslc info: %w: %s", err, oneLine(stderr))
	}
	var info struct {
		Server struct {
			Sessions []struct {
				Name string `json:"Name"`
			} `json:"Sessions"`
		} `json:"Server"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return nil, fmt.Errorf("top: wslc info printed something unexpected: %w", err)
	}
	var names []string
	for _, s := range info.Server.Sessions {
		if s.Name != "" {
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
