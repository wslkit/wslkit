//go:build windows

package collect

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/wslkit/wslkit/internal/winapi/console"
)

// Which distributions are running is one of the few facts the registry cannot
// answer, and it is needed by any check that wants to read a file inside a
// distribution over \\wsl.localhost without starting one.
//
// `wsl --list --running` was assumed to be off-limits, on the grounds that
// invoking wsl.exe might start the utility VM. Measured against a genuinely
// stopped VM, it does not: the query returns, the VM stays down, and it is
// still down seconds later. It is answered by the service, which is always
// running and tracks the state itself.
//
// So this is a passive signal after all, and it is the one used. See
// docs/research for the measurement.

// runningTimeout bounds the query. It talks to a service that is already
// running and answers immediately; anything longer than this means something is
// wrong with WSL itself, and a check that cannot say is better than a command
// that hangs.
const runningTimeout = 5 * time.Second

// runningDistros returns the set of distributions that are up.
//
// The name-only form is deliberate. `wsl --list --verbose` reports the state of
// every distribution in one call, which is richer, but its header and its
// "Running" and "Stopped" words are localised, so parsing them would make the
// signal wrong on a non-English Windows in exchange for nothing.
func runningDistros(ctx context.Context, timeout time.Duration) (map[string]bool, error) {
	if timeout <= 0 {
		timeout = runningTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "wsl.exe", "--list", "--running", "--quiet")
	console.OwnConsole(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()

	names := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(decodeWSL(out.Bytes()), "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			names[t] = true
		}
	}
	// Nothing running is reported as a non-zero exit with no output, which is a
	// successful answer of "none" rather than a failure. Only treat the error
	// as real when it came with nothing to read.
	if runErr != nil && len(names) == 0 {
		if cctx.Err() != nil {
			return nil, cctx.Err()
		}
		// An exit code with empty output is the "none" case. Anything that
		// wrote to stderr is a genuine problem worth reporting.
		if msg := strings.TrimSpace(decodeWSL(errb.Bytes())); msg != "" {
			return nil, &wslError{msg: msg}
		}
	}
	return names, nil
}

type wslError struct{ msg string }

func (e *wslError) Error() string { return "wsl --list --running: " + e.msg }

// decodeWSL turns whatever wsl.exe wrote into a Go string.
//
// The encoding is conditional: output produced inside a distribution is UTF-8,
// but wsl.exe writes its own output and its own diagnostics as UTF-16LE. An
// embedded NUL in an otherwise ASCII stream is the tell, and decoding the wrong
// way truncates the text at its first character.
func decodeWSL(b []byte) string {
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
