//go:build windows

package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/winapi/console"
	"github.com/wslkit/wslkit/internal/winapi/fileinfo"
	"github.com/wslkit/wslkit/internal/winapi/rstrtmgr"
)

func findWSLC() (string, error) {
	if p, err := exec.LookPath("wslc.exe"); err == nil {
		return p, nil
	}
	p := filepath.Join(os.Getenv("ProgramFiles"), "WSL", "wslc.exe")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", os.ErrNotExist
}

func runWSLC(ctx context.Context, exe string, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	console.OwnConsole(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// sessionRunning says whether a wslc session's VM is up, without asking wslc,
// which would boot it.
//
// While the VM runs its disk is attached, and the Restart Manager reports it
// held by System; while it is stopped, nobody holds it. Measured on WSL 2.9.12
// through several boots and idle stops. The Restart Manager holds no handle on
// the file, so asking cannot get in the way of a VM starting at that moment.
func sessionRunning(dir string) (bool, error) {
	held, err := rstrtmgr.Holders(filepath.Join(dir, "storage.vhdx"))
	if err != nil {
		return false, err
	}
	return len(held) > 0, nil
}

// collectWSLC tests the DNS each running wslc session hands its containers.
//
// It starts nothing. Sessions are found as directories under
// %LOCALAPPDATA%\wslc\sessions, and wslc is asked only about those whose VM
// is already running; a stopped one is recorded as such and left stopped.
func collectWSLC(ctx context.Context, e *env.Env, o Options) error {
	const src = `%LOCALAPPDATA%\wslc\sessions, Restart Manager, wslc system session run`
	exe, err := findWSLC()
	if err != nil {
		e.WSLC = env.Absent[env.WSLCInfo]("wslc.exe (ships with the WSL 2.9 pre-releases)")
		return nil
	}
	info := env.WSLCInfo{Sessions: []env.WSLCSession{}}
	if v, err := fileinfo.Version(exe); err == nil {
		info.Version = v
	}
	root := filepath.Join(os.Getenv("LOCALAPPDATA"), "wslc", "sessions")
	dirs, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		e.WSLC = env.Fail[env.WSLCInfo](kindOf(err), src, err)
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		s := env.WSLCSession{Name: name, Resolvers: []env.WSLCResolver{}}
		running, err := sessionRunning(filepath.Join(root, name))
		if err != nil {
			s.Err = "could not tell whether its VM is running, so it was left alone: " + err.Error()
			info.Sessions = append(info.Sessions, s)
			continue
		}
		if !running {
			info.Sessions = append(info.Sessions, s)
			continue
		}
		s.Running = true
		out, err := runWSLC(ctx, exe, wslcDNSScript, "--session", name, "system", "session", "run", "/bin/sh")
		parsed := parseWSLCDNS(name, out)
		parsed.Running = true
		if err != nil {
			parsed.Err = err.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				parsed.Err = "timed out waiting for the session VM"
			}
		}
		info.Sessions = append(info.Sessions, parsed)
	}
	e.WSLC = env.Ok(info, src)
	return nil
}
