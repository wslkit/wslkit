//go:build windows

package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/wslkit/wslkit/internal/env"
)

// /etc/wsl.conf lives inside a distribution and configures that distribution:
// whether systemd runs, how Windows drives are mounted, which user a shell
// starts as. It is where several confusing failures come from, and nothing on
// the Windows side reports it.
//
// It is readable from Windows over \\wsl.localhost\<name>\etc\wsl.conf, but
// only worth reaching for when the distribution is already running. Touching
// that path for a stopped distribution starts it, which is exactly what a
// read-only diagnosis must not do.

// wslConfTimeout bounds one read.
//
// Measured at a few tens of milliseconds on a healthy machine. The timeout is
// not for the normal case: it is for the one where the 9p server is wedged,
// which is a state a diagnostic tool meets more often than most, and where
// hanging would be worse than reporting that it could not read the file.
const wslConfTimeout = 3 * time.Second

// wslConfPath is where the file appears from Windows.
func wslConfPath(distro string) string {
	return `\\wsl.localhost\` + distro + `\etc\wsl.conf`
}

// readWslConf reads one distribution's wsl.conf.
//
// The read runs on its own goroutine because a hung network redirector does not
// respect a deadline on its own, and a diagnosis that never returns is worse
// than one that says it could not tell.
func readWslConf(ctx context.Context, distro string, timeout time.Duration) env.Field[string] {
	path := wslConfPath(distro)
	if timeout <= 0 {
		timeout = wslConfTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		b, err := os.ReadFile(path)
		done <- result{text: string(b), err: err}
	}()

	select {
	case <-cctx.Done():
		return env.Fail[string](env.ErrTimeout, path,
			fmt.Errorf("reading it took longer than %s, which usually means the distribution's file server is wedged", timeout))
	case r := <-done:
		switch {
		case r.err == nil:
			return env.Ok(r.text, path)
		case errors.Is(r.err, os.ErrNotExist):
			// Most distributions ship without one, and that is not a
			// fault: every setting in it has a default.
			return env.Absent[string](path)
		case errors.Is(r.err, os.ErrPermission):
			return env.Fail[string](env.ErrNeedsElevation, path, r.err)
		default:
			return env.Fail[string](env.ErrOther, path, r.err)
		}
	}
}

// readWslConfFor fills in the file for every distribution that is running.
//
// A stopped distribution is left with the reason recorded rather than a blank,
// so a check can say "not read because it is stopped" instead of reporting the
// defaults as though they were the file.
func readWslConfFor(ctx context.Context, distros []env.Distro, timeout time.Duration) {
	for i := range distros {
		d := &distros[i]
		if d.Version != 2 {
			// WSL 1 has a wsl.conf too, but not one reachable over this
			// path, and none of the checks that read it apply.
			d.WslConf = env.Fail[string](env.ErrUnsupported, wslConfPath(d.Name),
				errors.New("only read for WSL 2 distributions"))
			continue
		}
		if d.Running.ErrKind != env.ErrNone {
			d.WslConf = env.Fail[string](d.Running.ErrKind, wslConfPath(d.Name),
				errors.New("not read: whether the distribution is running could not be determined"))
			continue
		}
		if !d.Running.Value {
			d.WslConf = env.Fail[string](env.ErrVMWakeRefused, wslConfPath(d.Name),
				errors.New("not read: the distribution is stopped, and reading this path would start it"))
			continue
		}
		d.WslConf = readWslConf(ctx, d.Name, timeout)
	}
}
