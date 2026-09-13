//go:build windows

package disk

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
)

// WindowsHost runs wsl.exe.
type WindowsHost struct {
	// Exe is the executable to run, overridable for tests.
	Exe string
}

// NewHost returns the production host wrapper.
func NewHost() *WindowsHost { return &WindowsHost{Exe: "wsl.exe"} }

func (h *WindowsHost) exe() string {
	if h.Exe == "" {
		return "wsl.exe"
	}
	return h.Exe
}

// controlTimeout bounds the wsl.exe calls that do not enter a distribution.
const controlTimeout = 60 * time.Second

// Running lists the distributions that are up.
//
// wsl.exe exits non-zero with empty output when nothing is running, which is a
// successful answer of "none" rather than a failure.
func (h *WindowsHost) Running(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	out, _, code, err := h.run(cctx, "--list", "--running", "--quiet")
	names := splitNames(out)
	if len(names) == 0 && (code != 0 || err != nil) {
		// Nothing running. wsl.exe says so with an exit code, not a list.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("disk: listing running distributions: %w", err)
	}
	return names, nil
}

func splitNames(out string) []string {
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			names = append(names, t)
		}
	}
	return names
}

// Terminate stops one distribution.
func (h *WindowsHost) Terminate(ctx context.Context, name string) error {
	cctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	_, stderr, code, err := h.run(cctx, "--terminate", name)
	if err != nil || code != 0 {
		return fmt.Errorf("disk: stopping %s: %w: %s", name, err, strings.TrimSpace(stderr))
	}
	return nil
}

// Shutdown stops the utility VM and with it every distribution.
func (h *WindowsHost) Shutdown(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	_, stderr, code, err := h.run(cctx, "--shutdown")
	if err != nil || code != 0 {
		return fmt.Errorf("disk: shutting WSL down: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// RunAsRoot executes a command inside a distribution as root.
func (h *WindowsHost) RunAsRoot(ctx context.Context, distro string, argv []string, timeout time.Duration) (CommandResult, error) {
	if len(argv) == 0 {
		return CommandResult{}, fmt.Errorf("disk: no command given to run in %s", distro)
	}
	if !strings.HasPrefix(argv[0], "/") {
		// wsl.exe does not search PATH for the executable it is asked to
		// exec, so a relative name fails inside the guest with a message
		// that does not say why.
		return CommandResult{}, fmt.Errorf("disk: the command to run in %s must be an absolute path, got %q", distro, argv[0])
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := append([]string{"-d", distro, "-u", "root", "--exec"}, argv...)
	stdout, stderr, code, err := h.run(cctx, args...)
	return CommandResult{ExitCode: code, Stdout: stdout, Stderr: stderr}, err
}

// run executes wsl.exe and decodes its output.
func (h *WindowsHost) run(ctx context.Context, args ...string) (stdout, stderr string, code int, err error) {
	cmd := exec.CommandContext(ctx, h.exe(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	stdout = decodeWSLOutput(out.Bytes())
	stderr = decodeWSLOutput(errb.Bytes())
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			return stdout, stderr, ee.ExitCode(), nil
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

// decodeWSLOutput turns whatever wsl.exe wrote into a Go string.
//
// The encoding is conditional. Output produced inside the guest is UTF-8, but a
// diagnostic wsl.exe prints before it ever reaches the guest is UTF-16LE. An
// embedded NUL in an otherwise ASCII stream is the tell; decoding the wrong way
// truncates the message at its first character.
func decodeWSLOutput(b []byte) string {
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

// Unregister removes a distribution from WSL.
//
// It deletes the disk along with the registration, and fires its notification
// only afterwards, so nothing can intercept it. That is why `wslkit disk trash`
// moves the disk out of the way first: by the time this runs there is nothing
// left for it to delete.
func (h *WindowsHost) Unregister(ctx context.Context, name string) error {
	cctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	_, stderr, code, err := h.run(cctx, "--unregister", name)
	if err != nil || code != 0 {
		return fmt.Errorf("disk: unregistering %s: %w: %s", name, err, strings.TrimSpace(stderr))
	}
	return nil
}

// ImportInPlace registers an existing disk as a distribution without copying
// it, which is how a trashed distribution comes back.
func (h *WindowsHost) ImportInPlace(ctx context.Context, name, vhdPath string) error {
	cctx, cancel := context.WithTimeout(ctx, importTimeout)
	defer cancel()
	_, stderr, code, err := h.run(cctx, "--import-in-place", name, vhdPath)
	if err != nil || code != 0 {
		return fmt.Errorf("disk: importing %s from %s: %w: %s", name, vhdPath, err, strings.TrimSpace(stderr))
	}
	return nil
}

// importTimeout is longer than the other control commands: an import registers
// a disk and starts the utility VM to look at it.
const importTimeout = 5 * time.Minute
