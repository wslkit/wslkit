//go:build windows

package collect

import (
	"context"
	"errors"
	"time"

	"github.com/wslkit/wslkit/internal/env"
)

var vmRunner scriptRunner = systemdRunner

// readVM fills e.VM from one running distribution.
func readVM(ctx context.Context, e *env.Env, distros []env.Distro, timeout time.Duration) {
	name, ok, kind, reason := pickVMDistro(distros)
	if !ok {
		e.VM = env.Fail[env.VMFacts](kind, "wsl.exe (VM facts script)", errors.New(reason))
		return
	}
	src := vmSource(name)
	budget := remainingBudget(ctx, timeout)
	if budget <= 0 {
		e.VM = env.Fail[env.VMFacts](env.ErrTimeout, src, errors.New("not read: no time left in the collector deadline"))
		return
	}
	out, err := vmRunner.Run(ctx, name, vmScript, budget)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			e.VM = env.Fail[env.VMFacts](env.ErrTimeout, src, err)
			return
		}
		e.VM = env.Fail[env.VMFacts](env.ErrOther, src, err)
		return
	}
	v, err := parseVM(out, name)
	if err != nil {
		e.VM = env.Fail[env.VMFacts](env.ErrOther, src, err)
		return
	}
	e.VM = env.Ok(v, src)
}
