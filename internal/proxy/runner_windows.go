//go:build windows

package proxy

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
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
	cmd.Stdin = strings.NewReader(script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return decodeGuest(out.Bytes()), fmt.Errorf("%w: %s", err, strings.TrimSpace(decodeGuest(errb.Bytes())))
	}
	return decodeGuest(out.Bytes()), nil
}

// decodeGuest turns whatever came back into a string.
//
// Output produced inside the distribution is UTF-8, but wsl.exe writes its own
// errors as UTF-16LE. An embedded NUL in otherwise ASCII text is the tell, and
// decoding the wrong way truncates the message at its first character.
func decodeGuest(b []byte) string {
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
