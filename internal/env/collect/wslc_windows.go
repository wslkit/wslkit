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
	"github.com/wslkit/wslkit/internal/winapi/wslcsess"
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

// collectWSLC tests the DNS each running wslc session hands its containers.
//
// It starts nothing: wslc is asked only about a session whose VM is already
// running, which wslcsess tells without asking wslc. A stopped one is
// recorded as such and left stopped.
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
	sessions, err := wslcsess.List()
	if err != nil {
		e.WSLC = env.Fail[env.WSLCInfo](kindOf(err), src, err)
		return err
	}
	for _, found := range sessions {
		s := env.WSLCSession{Name: found.Name, Resolvers: []env.WSLCResolver{}}
		if found.Err != nil {
			s.Err = "could not tell whether its VM is running, so it was left alone: " + found.Err.Error()
			info.Sessions = append(info.Sessions, s)
			continue
		}
		if !found.Running {
			info.Sessions = append(info.Sessions, s)
			continue
		}
		out, err := runWSLC(ctx, exe, wslcDNSScript, "--session", found.Name, "system", "session", "run", "/bin/sh")
		parsed := parseWSLCDNS(found.Name, out)
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
