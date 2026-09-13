package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func envWithWatchers(procs ...env.WatchProc) *env.Env {
	e := env.New("t")
	d := env.Distro{GUID: "{a}", Name: "Ubuntu", Version: 2, IsDefault: true}
	d.Running = env.Ok(true, "t")
	d.Watchers = env.Ok(procs, "t")
	e.Distros = env.Ok([]env.Distro{d}, "t")
	return e
}

func TestWatchersNoneIsFine(t *testing.T) {
	if r := (Watchers{}).Run(envWithWatchers()); r.Status != probe.OK {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

// The knob is the whole value of the finding: knowing a watch does not work is
// not much use without the one setting that makes it work.
func TestWatchersNamesTheKnob(t *testing.T) {
	r := (Watchers{}).Run(envWithWatchers(env.WatchProc{Name: "vite", Dir: "/mnt/c/src/app"}))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "vite") || !strings.Contains(r.Summary, "/mnt/c/src/app") {
		t.Errorf("the summary should name the tool and the directory: %q", r.Summary)
	}
	if !strings.Contains(r.Detail, "usePolling") {
		t.Errorf("the detail should carry vite's own setting: %q", r.Detail)
	}
}

// A machine mid-build can have a dozen; the summary has to stay one line.
func TestWatchersSummaryStaysShort(t *testing.T) {
	var procs []env.WatchProc
	for _, d := range []string{"a", "b", "c", "d", "e"} {
		procs = append(procs, env.WatchProc{Name: "node", Dir: "/mnt/c/" + d})
	}
	r := (Watchers{}).Run(envWithWatchers(procs...))
	if !strings.Contains(r.Summary, "and 2 more") {
		t.Errorf("summary = %q", r.Summary)
	}
	if !strings.Contains(r.Detail, "/mnt/c/e") {
		t.Errorf("the detail should still have them all: %q", r.Detail)
	}
}

// A stopped distribution has nothing running in it to report, which is not the
// same as having nothing wrong.
func TestWatchersStoppedIsSkipped(t *testing.T) {
	e := envWithWatchers()
	list := e.DistroList()
	list[0].Running = env.Ok(false, "t")
	list[0].Watchers = env.Fail[[]env.WatchProc](env.ErrVMWakeRefused, "p", errStopped)
	e.Distros = env.Ok(list, "t")
	r := (Watchers{}).Run(e)
	if r.Status != probe.Skipped {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}
