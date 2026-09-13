package collect

import (
	"context"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/wslpath"
)

// Saving a download into \\wsl.localhost writes the mark of the web, which on
// NTFS is an alternate data stream attached to the file. The file server behind
// that path has no alternate streams, so the stream lands as a second, ordinary
// Linux file next to the first, named after it with a :Zone.Identifier suffix.
//
// The result is a distribution quietly accumulating files that only appear from
// inside Linux, that no Windows tool shows, and that scripts and build tools
// then trip over. It is one of the most-reacted-to open issues WSL has, and
// nothing reports it.
//
// The walk itself is here, over an fs.FS, so it can be tested without a
// distribution. What it walks, and the refusal to walk a stopped one, is in the
// Windows file next to this one.

// ZoneSuffix is what the stream becomes once it is a file.
const ZoneSuffix = ":Zone.Identifier"

// The walk is bounded three ways, because a home directory can hold a checked
// out monorepo, a node_modules tree, or both.
const (
	// zoneMaxDepth is how far below each root to look. Downloads land near
	// the top; a deeper walk costs much more and finds very little.
	zoneMaxDepth = 6
	// zoneMaxEntries stops a walk that has wandered into something enormous.
	zoneMaxEntries = 200000
	// zoneMaxPaths is how many paths to keep. A few are enough to report,
	// but the fix deletes exactly the paths recorded here rather than
	// re-deciding for itself what to remove, so the cap is set well above
	// what a report needs and a run that hits it says so.
	zoneMaxPaths = 500
)

// zoneRoots are where a saved download actually lands. Walking the whole
// filesystem would be slow, would descend into /mnt and back out over the
// network, and would report files the user never put there.
var zoneRoots = []string{"home", "root"}

// zoneSkip are directories not worth descending into: large, machine-generated,
// and not where a person saves a download from a browser.
var zoneSkip = map[string]bool{
	"node_modules": true,
	".git":         true,
	".cache":       true,
	".cargo":       true,
	".rustup":      true,
	".nvm":         true,
	"venv":         true,
	".venv":        true,
	"__pycache__":  true,
	"vendor":       true,
	"target":       true,
}

// zoneWalk accumulates a scan. The walk runs on its own goroutine and may still
// be running when the deadline passes, so every field is behind the mutex: what
// it has found so far is reported rather than thrown away.
type zoneWalk struct {
	mu      sync.Mutex
	scan    env.ZoneScan
	entries int
}

func (w *zoneWalk) found(p string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.scan.Count++
	if len(w.scan.Paths) < zoneMaxPaths {
		w.scan.Paths = append(w.scan.Paths, p)
	}
}

func (w *zoneWalk) truncate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.scan.Truncated = true
}

// step counts one entry and reports whether the walk may carry on.
func (w *zoneWalk) step() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries++
	if w.entries > zoneMaxEntries {
		w.scan.Truncated = true
		return false
	}
	return true
}

func (w *zoneWalk) result() env.ZoneScan {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.scan
	s.Paths = append([]string(nil), w.scan.Paths...)
	return s
}

// walkZoneRoot walks one root of one distribution, recording what it finds
// against the path inside the distribution.
func walkZoneRoot(ctx context.Context, fsys fs.FS, root string, w *zoneWalk) {
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// One unreadable directory must not end the walk: a home
			// directory holding another user's files is ordinary.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !w.step() {
			return fs.SkipAll
		}
		if d.IsDir() {
			if p != "." && (zoneSkip[path.Base(p)] || strings.Count(p, "/")+1 > zoneMaxDepth) {
				return fs.SkipDir
			}
			return nil
		}
		// The name has to be read back into Linux spelling before it is
		// matched. WSL stores the colon as a private use character, so a
		// file Linux calls "a:Zone.Identifier" arrives here without a
		// colon in it at all, and a plain suffix test finds nothing. That
		// is the whole bug reported as fixed.
		if strings.HasSuffix(wslpath.ToLinux(d.Name()), ZoneSuffix) {
			// Recorded as the path inside the distribution, which is
			// where the user has to act on it.
			w.found("/" + root + "/" + wslpath.ToLinux(p))
		}
		return nil
	})
}
