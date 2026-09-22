//go:build windows

package top

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
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
	// A console of the child's own, never shown. Sharing top's console let
	// wsl.exe switch it to Linux terminal semantics, where a line feed does
	// not return to the first column, and top's own report then came out as
	// a staircase. Its standard streams are pipes either way.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return decode(out.Bytes()), decode(errb.Bytes()), err
}

// decode turns whatever wsl.exe wrote into a Go string.
//
// The encoding is conditional: output produced inside the guest is UTF-8, but a
// diagnostic wsl.exe prints before it reaches the guest is UTF-16LE. An
// embedded NUL in an otherwise ASCII stream is the tell.
func decode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if !bytes.ContainsRune(b, 0) {
		return string(b)
	}
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
