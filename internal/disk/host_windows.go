//go:build windows

package disk

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
// With nothing running, --quiet exits 0 with empty output (measured on 3.0.3),
// and without it wsl.exe exits non-zero and says so. Either is a successful
// answer of "none" rather than a failure.
func (h *WindowsHost) Running(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	out, _, code, err := h.run(cctx, "--list", "--running", "--quiet")
	names := splitNames(out)
	if len(names) == 0 && (code != 0 || err != nil) {
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
	console.OwnConsole(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	stdout = wslout.Decode(out.Bytes())
	stderr = wslout.Decode(errb.Bytes())
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			return stdout, stderr, ee.ExitCode(), nil
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

// Mount runs `wsl.exe --mount ...` for disk automount. It needs an elevated
// process; wsl.exe says so itself when it is not, and the caller checks first.
func (h *WindowsHost) Mount(ctx context.Context, args []string) (CommandResult, error) {
	cctx, cancel := context.WithTimeout(ctx, importTimeout)
	defer cancel()
	stdout, stderr, code, err := h.run(cctx, args...)
	return CommandResult{ExitCode: code, Stdout: stdout, Stderr: stderr}, err
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

// Export writes a distribution's filesystem to a tar archive.
//
// The distribution is stopped by wsl.exe first if it is running, so the archive
// is consistent. It is a plain tar, not a disk image, which is exactly the
// point: rebuilding from it lays every file down afresh.
func (h *WindowsHost) Export(ctx context.Context, name, tarPath string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, stderr, code, err := h.run(cctx, "--export", name, tarPath)
	if err != nil || code != 0 {
		return fmt.Errorf("disk: exporting %s to %s: %w: %s", name, tarPath, err, strings.TrimSpace(stderr))
	}
	return nil
}

// ImportTar registers a distribution from a tar archive, creating a new disk in
// the given directory.
//
// --version 2 is passed explicitly rather than relying on the machine's default
// version, which a user can set to 1 and forget: importing a WSL 2
// distribution as WSL 1 would put its files on NTFS and quietly change how
// everything about it behaves.
func (h *WindowsHost) ImportTar(ctx context.Context, name, dir, tarPath string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, stderr, code, err := h.run(cctx, "--import", name, dir, tarPath, "--version", "2")
	if err != nil || code != 0 {
		return fmt.Errorf("disk: importing %s from %s: %w: %s", name, tarPath, err, strings.TrimSpace(stderr))
	}
	return nil
}
