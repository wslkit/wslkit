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

// collectWSLC tests the DNS each wslc session hands its containers. Only with
// doctor --wslc: it runs a command in the session VM, which boots it.
//
// It never creates a session. `wslc info` lists the existing ones without
// starting anything (measured on 2.9.12); a machine with none gets an empty
// list, not a new session.
func collectWSLC(ctx context.Context, e *env.Env, o Options) error {
	if !o.WSLC {
		return nil
	}
	const src = "wslc info, wslc system session run"
	exe, err := findWSLC()
	if err != nil {
		e.WSLC = env.Absent[env.WSLCInfo]("wslc.exe (ships with the WSL 2.9 pre-releases)")
		return nil
	}
	out, err := runWSLC(ctx, exe, "", "info", "--format", "json")
	if err != nil {
		e.WSLC = env.Fail[env.WSLCInfo](kindOf(err), src, err)
		return err
	}
	version, names, err := parseWSLCSessions(out)
	if err != nil {
		e.WSLC = env.Fail[env.WSLCInfo](env.ErrOther, src, err)
		return err
	}
	info := env.WSLCInfo{Version: version, Sessions: []env.WSLCSession{}}
	for _, name := range names {
		out, err := runWSLC(ctx, exe, wslcDNSScript, "--session", name, "system", "session", "run", "/bin/sh")
		s := parseWSLCDNS(name, out)
		if err != nil {
			s.Err = err.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				s.Err = "timed out waiting for the session VM"
			}
		}
		info.Sessions = append(info.Sessions, s)
	}
	e.WSLC = env.Ok(info, src)
	return nil
}
