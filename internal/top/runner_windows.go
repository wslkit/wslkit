//go:build windows

package top

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/winapi/console"
	"github.com/wslkit/wslkit/internal/wslout"
)

// WSLRunner is the production Runner.
type WSLRunner struct{ Exe string }

func (r WSLRunner) exe() string {
	if r.Exe == "" {
		return "wsl.exe"
	}
	return r.Exe
}

// Running lists the distributions that are up.
//
// wsl.exe exits non-zero with empty output when nothing is running, which is a
// successful answer of "none" rather than a failure.
func (r WSLRunner) Running(ctx context.Context) ([]string, error) {
	out, _, _ := r.run(ctx, 30*time.Second, nil, "--list", "--running", "--quiet")
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			names = append(names, t)
		}
	}
	return names, nil
}

// Sample runs the measurement script inside one distribution.
//
// The script is fed on standard input rather than passed as an argument: it is
// long, and quoting it through wsl.exe and then through the guest shell is a
// way to introduce a bug that shows up on one distribution and not the others.
func (r WSLRunner) Sample(ctx context.Context, distro string, timeout time.Duration) (string, error) {
	out, stderr, err := r.run(ctx, timeout, []byte(SampleScript), "-d", distro, "-u", "root", "--exec", "/bin/sh")
	if err != nil {
		return out, fmt.Errorf("top: measuring %s: %w: %s", distro, err, oneLine(stderr))
	}
	return out, nil
}

func (r WSLRunner) run(ctx context.Context, timeout time.Duration, stdin []byte, args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, r.exe(), args...)
	// top runs one of these per distribution at once, which is exactly the
	// overlap that leaves a shared console broken.
	console.OwnConsole(cmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return wslout.Decode(out.Bytes()), wslout.Decode(errb.Bytes()), err
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
