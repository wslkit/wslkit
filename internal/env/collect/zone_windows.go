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

// Finding the :Zone.Identifier files means walking a distribution's home
// directories from Windows. That is only done for a distribution that is
// already running: reaching into a stopped one over this path starts it, which
// a read-only diagnosis must not do. The walk itself is in zone.go.

// scanZoneFiles walks one running distribution looking for the stream files.
//
// A deadline is not a failure here. A partial count is still a true statement
// that there are at least this many, which is all the finding needs, so the
// timeout is reported as a truncated result rather than an error.
func scanZoneFiles(ctx context.Context, distro string, timeout time.Duration) env.Field[env.ZoneScan] {
	base := zonePath(distro)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	w := &zoneWalk{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, root := range zoneRoots {
			start := base + `\` + root
			if _, err := os.Stat(start); err != nil {
				// A distribution with no /home or no /root is unusual
				// but not a fault, and neither is one whose file
				// server refused this particular directory.
				continue
			}
			walkZoneRoot(cctx, os.DirFS(start), root, w)
		}
	}()

	select {
	case <-cctx.Done():
		// The goroutine is abandoned rather than waited for: a wedged file
		// server does not respect a deadline, and the walk holds nothing
		// anybody else needs.
		w.truncate()
		return env.Ok(w.result(), base)
	case <-done:
		return env.Ok(w.result(), base)
	}
}

// zonePath is the root of the scan for one distribution.
func zonePath(distro string) string { return `\\wsl.localhost\` + distro }

// zoneMargin is what the scan leaves of the collector's deadline for the
// collector to finish in.
const zoneMargin = 250 * time.Millisecond

// zoneBudget is how long the scan may take.
//
// This is the last thing the distribution collector does, and it runs under
// that collector's deadline. Taking the full per-collector timeout would push
// past it and have the whole collector abandoned, losing the registry inventory
// that everything else depends on over a walk that is only a nice-to-have. So
// the scan gets what is left, not what it was offered.
func zoneBudget(ctx context.Context, timeout time.Duration) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return timeout
	}
	left := time.Until(dl) - zoneMargin
	if left < timeout {
		return left
	}
	return timeout
}

// scanZoneFilesFor fills in the scan for every distribution that is running.
//
// The distributions are scanned concurrently. Sequentially they would share one
// collector deadline between them, so a machine with several running
// distributions would report on the first and give up on the rest.
func scanZoneFilesFor(ctx context.Context, distros []env.Distro, timeout time.Duration) {
	budget := zoneBudget(ctx, timeout)
	var wg sync.WaitGroup
	for i := range distros {
		d := &distros[i]
		switch {
		case d.Version != 2:
			// A WSL 1 distribution stores its files on NTFS, where the
			// stream stays a stream. The problem does not arise.
			d.ZoneFiles = env.Fail[env.ZoneScan](env.ErrUnsupported, zonePath(d.Name),
				errors.New("only scanned for WSL 2 distributions"))
		case d.Running.ErrKind != env.ErrNone:
			d.ZoneFiles = env.Fail[env.ZoneScan](d.Running.ErrKind, zonePath(d.Name),
				errors.New("not scanned: whether the distribution is running could not be determined"))
		case !d.Running.Value:
			d.ZoneFiles = env.Fail[env.ZoneScan](env.ErrVMWakeRefused, zonePath(d.Name),
				errors.New("not scanned: the distribution is stopped, and reading this path would start it"))
		case budget <= 0:
			// The collector has already used its deadline on the rest of
			// the inventory. Saying so beats reporting a clean tree
			// nobody walked.
			d.ZoneFiles = env.Fail[env.ZoneScan](env.ErrTimeout, zonePath(d.Name),
				errors.New("not scanned: no time left in the collector deadline"))
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.ZoneFiles = scanZoneFiles(ctx, d.Name, budget)
			}()
		}
	}
	wg.Wait()
}
