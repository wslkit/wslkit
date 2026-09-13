//go:build windows

package collect

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/wslkit/wslkit/internal/env"
)

// Reading /proc means reading the distribution from Windows, so the same rule
// applies as everywhere else here: only one that is already running, because
// reaching into a stopped one would start it. The matching itself is in
// watchers.go.

// readWatchers scans one running distribution's /proc.
func readWatchers(ctx context.Context, distro string, timeout time.Duration) env.Field[[]env.WatchProc] {
	src := zonePath(distro) + `\proc`
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		procs []env.WatchProc
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		if _, err := os.Stat(src); err != nil {
			done <- outcome{err: err}
			return
		}
		done <- outcome{procs: scanWatchers(cctx, os.DirFS(src))}
	}()

	select {
	case <-cctx.Done():
		// The goroutine is abandoned rather than waited for: a wedged file
		// server does not respect a deadline.
		return env.Fail[[]env.WatchProc](env.ErrTimeout, src,
			errors.New("reading /proc took longer than "+timeout.String()))
	case r := <-done:
		if r.err != nil {
			return env.Fail[[]env.WatchProc](kindOf(r.err), src, r.err)
		}
		return env.Ok(r.procs, src)
	}
}

// readWatchersFor fills in the scan for every distribution that is running.
func readWatchersFor(ctx context.Context, distros []env.Distro, timeout time.Duration) {
	// Shares the zone scan's reasoning about the collector deadline: this is
	// optional work at the end of a collector whose main job is the registry
	// inventory, and it takes what is left rather than what it was offered.
	budget := remainingBudget(ctx, timeout)
	var wg sync.WaitGroup
	for i := range distros {
		d := &distros[i]
		switch {
		case d.Version != 2:
			// WSL 1 has no /mnt in this sense: its files are on NTFS
			// directly and there is no 9p server in the way.
			d.Watchers = env.Fail[[]env.WatchProc](env.ErrUnsupported, zonePath(d.Name),
				errors.New("only read for WSL 2 distributions"))
		case d.Running.ErrKind != env.ErrNone:
			d.Watchers = env.Fail[[]env.WatchProc](d.Running.ErrKind, zonePath(d.Name),
				errors.New("not read: whether the distribution is running could not be determined"))
		case !d.Running.Value:
			d.Watchers = env.Fail[[]env.WatchProc](env.ErrVMWakeRefused, zonePath(d.Name),
				errors.New("not read: the distribution is stopped, and nothing is running in it to report"))
		case budget <= 0:
			d.Watchers = env.Fail[[]env.WatchProc](env.ErrTimeout, zonePath(d.Name),
				errors.New("not read: no time left in the collector deadline"))
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.Watchers = readWatchers(ctx, d.Name, budget)
			}()
		}
	}
	wg.Wait()
}
