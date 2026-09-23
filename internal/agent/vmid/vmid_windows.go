//go:build windows

// Package vmid discovers the running WSL 2 utility VM's id without waking it:
// WSL's own user-mode helpers (wslhost.exe, wslrelay.exe) are started with
// "--vm-id {GUID}" on their command line, readable through WMI for processes
// owned by the current user. The id changes on every `wsl --shutdown`.
package vmid

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/winapi/console"
	"github.com/wslkit/wslkit/internal/winapi/wmi"
)

var reVMID = regexp.MustCompile(`(?i)--vm-id\s*\{?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\}?`)

// ErrNotRunning means no WSL helper process advertises a VM id.
var ErrNotRunning = errors.New("vmid: no running WSL VM found (no wslhost.exe/wslrelay.exe with --vm-id)")

// Passive returns the VM id from running helper processes, or ErrNotRunning.
func Passive(ctx context.Context) (string, error) {
	rows, err := wmi.Query(ctx, `root\cimv2`, "SELECT CommandLine FROM Win32_Process WHERE Name='wslhost.exe' OR Name='wslrelay.exe'", "CommandLine")
	if err != nil {
		return "", fmt.Errorf("vmid: %w", err)
	}
	for _, r := range rows {
		if m := reVMID.FindStringSubmatch(fmt.Sprint(r["CommandLine"])); m != nil {
			return strings.ToLower(m[1]), nil
		}
	}
	return "", ErrNotRunning
}

// Active asks the distro itself (wslinfo --vm-id). This starts the distro and
// the VM if they are not running, so callers must have the user's consent.
func Active(ctx context.Context, distro string) (string, error) {
	args := []string{"--", "wslinfo", "--vm-id"}
	if distro != "" {
		args = append([]string{"-d", distro}, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "wsl.exe", args...)
	console.OwnConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("vmid: wslinfo --vm-id: %w", err)
	}
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(string(out), "\x00", "")))
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(s) {
		return "", fmt.Errorf("vmid: unexpected wslinfo output %q", s)
	}
	return s, nil
}

// ParseCommandLine extracts a VM id from a helper command line (exported for tests).
func ParseCommandLine(cmdline string) (string, bool) {
	m := reVMID.FindStringSubmatch(cmdline)
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[1]), true
}
