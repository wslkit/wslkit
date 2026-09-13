package collect

import (
	"context"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
)

// A file watcher does not work across the Windows filesystem. inotify is a
// kernel facility over a real Linux filesystem; /mnt/c is a protocol to a
// server on the Windows side that never tells anyone a file changed. A watch
// set there is accepted, and then simply never fires.
//
// Nothing reports it. The build tool waits, the page does not reload, and the
// developer concludes the tool is broken. Upstream has been clear that user
// land cannot fix it, so the useful thing to do is notice the situation and
// name the one setting that works: polling.
//
// Noticing it means finding processes whose working directory is on /mnt. The
// working directory itself cannot be read from Windows, because the file server
// refuses to resolve the /proc/<pid>/cwd symlink, but PWD in the process
// environment can be, and for a tool started from a shell that is the same
// directory. The reading of /proc is over an fs.FS so the matching can be
// tested without a distribution; the Windows file next to this one supplies
// the real one.

// watchMaxProcs bounds the scan. A distribution with more processes than this
// has something else going on.
const watchMaxProcs = 4000

// watchMaxFindings is how many to record. The advice is the same for all of
// them, so a longer list adds nothing.
const watchMaxFindings = 20

// scanWatchers reads a /proc and returns the watcher processes working on the
// Windows filesystem.
func scanWatchers(ctx context.Context, procfs fs.FS) []env.WatchProc {
	tbl, err := data.LoadWatchers()
	if err != nil {
		return nil
	}
	entries, err := fs.ReadDir(procfs, ".")
	if err != nil {
		return nil
	}
	var out []env.WatchProc
	seen := map[string]bool{}
	n := 0
	for _, e := range entries {
		if ctx.Err() != nil || len(out) >= watchMaxFindings {
			break
		}
		if !e.IsDir() || !isPID(e.Name()) {
			continue
		}
		if n++; n > watchMaxProcs {
			break
		}
		p, ok := readWatchProc(procfs, e.Name(), tbl)
		if !ok {
			continue
		}
		// One tool in one directory is one finding, however many worker
		// processes it has forked.
		key := p.Name + "\x00" + p.Dir
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// readWatchProc decides whether one process is worth reporting.
func readWatchProc(procfs fs.FS, pid string, tbl *data.Watchers) (env.WatchProc, bool) {
	comm := strings.TrimSpace(readProcFile(procfs, pid, "comm"))
	if comm == "" {
		return env.WatchProc{}, false
	}
	args := splitNUL(readProcFile(procfs, pid, "cmdline"))

	name, ok := watcherName(args, comm, tbl)
	if !ok {
		return env.WatchProc{}, false
	}

	// The environment is only readable for the user's own processes, which
	// is exactly the set that matters: a watcher is something a person
	// started, not something the system did.
	dir := envValue(readProcFile(procfs, pid, "environ"), "PWD")
	if !onWindowsDrive(dir) {
		// PWD is missing for a process that was not started from a shell,
		// and stale for one that changed directory afterwards. An argument
		// naming a path on /mnt says the same thing, and says it about the
		// files rather than about the shell.
		dir = ""
		for _, a := range args {
			if onWindowsDrive(a) {
				dir = path.Dir(a)
				break
			}
		}
		if dir == "" {
			return env.WatchProc{}, false
		}
	}
	return env.WatchProc{Name: name, Dir: dir}, true
}

// watcherName decides what to call a process, most specific name first.
//
// The command name of anything written in a scripting language is the
// interpreter: vite, webpack and the rest all run as "node". Reporting "node"
// would send the reader looking for the wrong setting, so the script being run
// wins over the thing running it, and both win over the command name.
func watcherName(args []string, comm string, tbl *data.Watchers) (string, bool) {
	var names []string
	if len(args) > 1 {
		names = append(names, path.Base(strings.TrimSpace(args[1])))
	}
	if len(args) > 0 {
		names = append(names, path.Base(strings.TrimSpace(args[0])))
	}
	names = append(names, comm)
	for _, n := range names {
		if _, ok := tbl.LookupWatcher(n); ok {
			return n, true
		}
	}
	return "", false
}

// onWindowsDrive reports whether a path is on a mounted Windows drive.
//
// The automount root is configurable, so this is not the whole truth. It is the
// default that all but a handful of machines use, and a check that reported
// nothing on the rest would be worse than one that misses a renamed /mnt.
func onWindowsDrive(p string) bool {
	if !strings.HasPrefix(p, "/mnt/") || len(p) < 7 {
		return false
	}
	drive := p[5]
	return drive >= 'a' && drive <= 'z' && p[6] == '/'
}

func isPID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func readProcFile(procfs fs.FS, pid, name string) string {
	b, err := fs.ReadFile(procfs, pid+"/"+name)
	if err != nil {
		return ""
	}
	return string(b)
}

// splitNUL splits the NUL-separated files under /proc.
func splitNUL(s string) []string {
	var out []string
	for _, f := range strings.Split(s, "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// envValue picks one variable out of a process environment.
func envValue(environ, key string) string {
	for _, kv := range strings.Split(environ, "\x00") {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}
