//go:build windows

package collect

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/proxy"
)

// scriptRunner runs a script inside a distribution as root. proxy.WSLRunner is
// the one limit and proxy already use; tests substitute their own.
type scriptRunner interface {
	Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error)
}

var systemdRunner scriptRunner = proxy.WSLRunner{}

// readSystemd runs the script in one running distribution.
func readSystemd(ctx context.Context, d env.Distro, timeout time.Duration) env.Field[env.SystemdState] {
	src := systemdSource(d.Name)
	out, err := systemdRunner.Run(ctx, d.Name, systemdScript(d.DefaultUid), timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return env.Fail[env.SystemdState](env.ErrTimeout, src, err)
		}
		return env.Fail[env.SystemdState](env.ErrOther, src, err)
	}
	s, err := parseSystemd(out, d.DefaultUid)
	if err != nil {
		return env.Fail[env.SystemdState](env.ErrOther, src, err)
	}
	return env.Ok(s, src)
}

// readSystemdFor fills in the state for every distribution that is running.
//
// Only running ones: starting a distribution to ask whether its systemd is
// healthy would start systemd, which is the thing being asked about.
func readSystemdFor(ctx context.Context, distros []env.Distro, timeout time.Duration) {
	budget := remainingBudget(ctx, timeout)
	var wg sync.WaitGroup
	for i := range distros {
		d := &distros[i]
		src := systemdSource(d.Name)
		switch {
		case d.Version != 2:
			d.Systemd = env.Fail[env.SystemdState](env.ErrUnsupported, src,
				errors.New("only read for WSL 2 distributions"))
		case d.Running.ErrKind != env.ErrNone:
			d.Systemd = env.Fail[env.SystemdState](d.Running.ErrKind, src,
				errors.New("not read: whether the distribution is running could not be determined"))
		case !d.Running.Value:
			d.Systemd = env.Fail[env.SystemdState](env.ErrVMWakeRefused, src,
				errors.New("not read: the distribution is stopped, and starting it would start systemd"))
		case budget <= 0:
			d.Systemd = env.Fail[env.SystemdState](env.ErrTimeout, src,
				errors.New("not read: no time left in the collector deadline"))
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.Systemd = readSystemd(ctx, *d, budget)
			}()
		}
	}
	wg.Wait()
}
