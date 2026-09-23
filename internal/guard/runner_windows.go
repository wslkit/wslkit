//go:build windows

package guard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/wslkit/wslkit/internal/winapi/console"
)

// WSLRunner is the real machine.
//
// Every call is bounded, and a timeout is reported as context.DeadlineExceeded
// rather than as an ordinary error, because the whole design turns on the
// difference: a service that refuses is not a service that has stopped
// answering, and only the second one is worth restarting anything for.
type WSLRunner struct{}

func (WSLRunner) Now() time.Time        { return time.Now() }
func (WSLRunner) Sleep(d time.Duration) { time.Sleep(d) }

// run executes wsl.exe with a deadline.
func (WSLRunner) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "wsl.exe", args...)
	console.OwnConsole(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	text := decodeWSL(out.Bytes())
	if cctx.Err() != nil {
		// The deadline is the finding. Whatever the process reported on its
		// way out is noise beside it.
		return text, fmt.Errorf("wsl %s: %w", strings.Join(args, " "), context.DeadlineExceeded)
	}
	if err != nil {
		msg := strings.TrimSpace(decodeWSL(errb.Bytes()))
		if msg == "" {
			msg = strings.TrimSpace(text)
		}
		return text, fmt.Errorf("wsl %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return text, nil
}

func (r WSLRunner) Status(ctx context.Context, timeout time.Duration) error {
	_, err := r.run(ctx, timeout, "--status")
	return err
}

// Exec runs the cheapest possible command inside a distribution. /bin/true does
// nothing at all, so anything it takes is the distribution answering rather than
// the command running.
func (r WSLRunner) Exec(ctx context.Context, distro string, timeout time.Duration) error {
	_, err := r.run(ctx, timeout, "-d", distro, "--exec", "/bin/true")
	return err
}

func (r WSLRunner) DefaultDistro(ctx context.Context) (string, error) {
	out, err := r.run(ctx, StatusTimeout, "--list", "--quiet")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		// The first name is the default: `wsl --list` lists it first.
		if t := strings.TrimSpace(line); t != "" {
			return t, nil
		}
	}
	return "", nil
}

func (r WSLRunner) Running(ctx context.Context) ([]string, error) {
	// Non-zero with empty output means "none running", which is an answer
	// rather than a failure.
	out, _ := r.run(ctx, StatusTimeout, "--list", "--running", "--quiet")
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			names = append(names, t)
		}
	}
	return names, nil
}

func (r WSLRunner) Shutdown(ctx context.Context, force bool, timeout time.Duration) error {
	args := []string{"--shutdown"}
	if force {
		// --force terminates the compute system directly rather than asking
		// the VM to stop, which is what makes it work when the vsock is
		// dead and the polite one hangs.
		args = append(args, "--force")
	}
	_, err := r.run(ctx, timeout, args...)
	return err
}

// KillService kills wslservice.exe, which upstream reports as the step that
// most often works. Administrator only.
//
// taskkill rather than the service control manager: a service that is not
// answering its control channel does not answer a stop request either, which is
// the situation this rung exists for.
func (WSLRunner) KillService(ctx context.Context, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "taskkill.exe", "/f", "/im", "wslservice.exe")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(decodeWSL(out.Bytes()))
		// "not found" means somebody else already stopped it, which is the
		// state this was trying to reach.
		if strings.Contains(strings.ToLower(text), "not found") {
			return nil
		}
		return fmt.Errorf("taskkill wslservice.exe: %w: %s", err, text)
	}
	return nil
}

// RestartService starts the service again properly.
//
// Only WSLService is touched. vmcompute and HvHost are never stopped or
// restarted by anything here: stopping vmcompute has been reported to bluescreen
// the machine, and no hung distribution is worth that.
func (WSLRunner) RestartService(ctx context.Context, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sc.exe", "start", "WSLService")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(decodeWSL(out.Bytes()))
		// 1056 is "an instance of the service is already running", which is
		// success by another name.
		if strings.Contains(text, "1056") {
			return nil
		}
		return fmt.Errorf("sc start WSLService: %w: %s", err, text)
	}
	return nil
}

func (r WSLRunner) GuestClock(ctx context.Context, distro string, timeout time.Duration) (time.Time, error) {
	out, err := r.run(ctx, timeout, "-d", distro, "--exec", "/bin/date", "+%s")
	if err != nil {
		return time.Time{}, err
	}
	secs, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if perr != nil {
		return time.Time{}, fmt.Errorf("guard: %s returned %q rather than a timestamp", distro, strings.TrimSpace(out))
	}
	return time.Unix(secs, 0), nil
}

// StepClock corrects the guest clock from the hardware clock.
//
// hwclock reads the emulated RTC, which the hypervisor keeps right, and steps
// the system clock to match. chronyd would eventually agree, but after its
// first three updates it slews rather than steps, so an offset built up over
// hours of sleep can take hours to close.
func (r WSLRunner) StepClock(ctx context.Context, distro string, timeout time.Duration) error {
	_, err := r.run(ctx, timeout, "-d", distro, "-u", "root", "--exec", "/sbin/hwclock", "-s")
	if err == nil {
		return nil
	}
	// Not every distribution has hwclock in /sbin, and some have none at
	// all; the busybox one lives elsewhere. One retry through a shell finds
	// it on PATH before giving up.
	_, err2 := r.run(ctx, timeout, "-d", distro, "-u", "root", "--exec", "/bin/sh", "-c", "command -v hwclock >/dev/null && hwclock -s")
	if err2 != nil {
		return errors.Join(err, err2)
	}
	return nil
}

// decodeWSL turns whatever wsl.exe wrote into a string. Its own output is
// UTF-16LE while anything from inside a distribution is UTF-8, and decoding the
// wrong way truncates the text at its first character.
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
