//go:build windows

package collect

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/winapi/console"
	"github.com/wslkit/wslkit/internal/wslout"
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
	for _, line := range strings.Split(strings.ReplaceAll(wslout.Decode(out.Bytes()), "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			names[t] = true
		}
	}
	// With --quiet, nothing running is exit 0 and no output; measured on 3.0.3,
	// and WslClient.cpp only returns -1 for it without --quiet. That means an
	// empty answer cannot be told from output that never arrived, which is what
	// microsoft/WSL#41937 was: security software ending wsl.exe before it
	// flushed. Only treat an error as real when it came with nothing to read.
	if runErr != nil && len(names) == 0 {
		if cctx.Err() != nil {
			return nil, cctx.Err()
		}
		// A non-zero exit with nothing written anywhere is still read as
		// "none", as it was before. Anything on stderr is a genuine problem.
		if msg := strings.TrimSpace(wslout.Decode(errb.Bytes())); msg != "" {
			return nil, &wslError{msg: msg}
		}
	}
	return names, nil
}

type wslError struct{ msg string }

func (e *wslError) Error() string { return "wsl --list --running: " + e.msg }
