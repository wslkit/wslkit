//go:build windows

package proxy

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

// WSLRunner runs a shell script inside a distribution through wsl.exe, as root.
//
// Root, because the file server behind \wsl.localhost maps to the
// distribution's default user and cannot write /etc at all on a distribution
// whose default user is not root.
//
// The script goes in on standard input rather than as an argument. Measured the
// other way round first: a script passed as an argument is quoted by Go for the
// Windows command line, unquoted by wsl.exe, and handed to the guest shell,
// and `tmp="$x"` came out the far end with the variable already gone. Nothing
// is quoted on this path because nothing is parsed by anything but the shell it
// is written for.
type WSLRunner struct{}

func (WSLRunner) Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "wsl.exe", "-d", distro, "-u", "root", "--exec", "/bin/sh")
	console.OwnConsole(cmd)
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return wslout.Decode(out.Bytes()), fmt.Errorf("%w: %s", err, strings.TrimSpace(wslout.Decode(errb.Bytes())))
	}
	return wslout.Decode(out.Bytes()), nil
}
