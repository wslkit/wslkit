package collect

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// proc builds a /proc for one process. environ is written the way the kernel
// writes it, NUL-separated, because that is what the reader has to cope with.
func proc(pid, comm string, args []string, environ map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{
		pid + "/comm": {Data: []byte(comm + "\n")},
	}
	if len(args) > 0 {
		fsys[pid+"/cmdline"] = &fstest.MapFile{Data: []byte(strings.Join(args, "\x00") + "\x00")}
	}
	if environ != nil {
		var kv []string
		for k, v := range environ {
			kv = append(kv, k+"="+v)
		}
		fsys[pid+"/environ"] = &fstest.MapFile{Data: []byte(strings.Join(kv, "\x00") + "\x00")}
	}
	return fsys
}

func merge(all ...fstest.MapFS) fstest.MapFS {
	out := fstest.MapFS{}
	for _, m := range all {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestScanWatchersFindsAToolOnAWindowsDrive(t *testing.T) {
	fsys := merge(
		proc("101", "node", []string{"node", "server.js"}, map[string]string{"PWD": "/mnt/c/src/app"}),
		proc("102", "bash", []string{"-bash"}, map[string]string{"PWD": "/mnt/c/src/app"}),
		proc("103", "node", []string{"node", "worker.js"}, map[string]string{"PWD": "/home/ana/api"}),
	)
	got := scanWatchers(context.Background(), fsys)
	if len(got) != 1 {
		t.Fatalf("got %+v, want one finding", got)
	}
	if got[0].Name != "node" || got[0].Dir != "/mnt/c/src/app" {
		t.Errorf("finding = %+v", got[0])
	}
}

// A shell is not a watcher and a watcher on the distribution's own filesystem
// is not a problem. Warning about either would spend the reader's attention on
// nothing.
func TestScanWatchersIgnoresTheUninteresting(t *testing.T) {
	fsys := merge(
		proc("1", "systemd", []string{"/sbin/init"}, nil),
		proc("2", "vite", []string{"node", "vite"}, map[string]string{"PWD": "/home/ana/app"}),
		proc("3", "sshd", []string{"sshd"}, map[string]string{"PWD": "/mnt/c/src"}),
	)
	if got := scanWatchers(context.Background(), fsys); len(got) != 0 {
		t.Fatalf("got %+v, want nothing", got)
	}
}

// The command name of a script is its interpreter. Reporting "node" when the
// user ran vite would send them looking for the wrong knob.
func TestScanWatchersNamesTheToolNotTheInterpreter(t *testing.T) {
	fsys := proc("200", "node", []string{"vite", "--host"}, map[string]string{"PWD": "/mnt/d/work/site"})
	got := scanWatchers(context.Background(), fsys)
	if len(got) != 1 || got[0].Name != "vite" {
		t.Fatalf("got %+v, want vite", got)
	}
}

// PWD is missing for anything not started from a shell, and stale for anything
// that changed directory. A path in the arguments says the same thing about the
// files themselves.
func TestScanWatchersFallsBackToTheArguments(t *testing.T) {
	fsys := proc("300", "hugo", []string{"hugo", "server", "-s", "/mnt/c/site/config.toml"}, nil)
	got := scanWatchers(context.Background(), fsys)
	if len(got) != 1 || got[0].Dir != "/mnt/c/site" {
		t.Fatalf("got %+v", got)
	}
}

// A dev server forks a worker per core. One tool in one directory is one
// problem, however many processes are doing it.
func TestScanWatchersDeduplicates(t *testing.T) {
	var all []fstest.MapFS
	for _, pid := range []string{"400", "401", "402", "403"} {
		all = append(all, proc(pid, "webpack", []string{"webpack", "serve"}, map[string]string{"PWD": "/mnt/c/app"}))
	}
	if got := scanWatchers(context.Background(), merge(all...)); len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
}

func TestOnWindowsDrive(t *testing.T) {
	yes := []string{"/mnt/c/", "/mnt/c/src", "/mnt/z/a/b"}
	no := []string{"", "/mnt", "/mnt/", "/mnt/c", "/mnt/cd/x", "/home/ana", "/mnt/wsl/share", "/mntx/c/a"}
	for _, s := range yes {
		if !onWindowsDrive(s) {
			t.Errorf("%q should be on a Windows drive", s)
		}
	}
	for _, s := range no {
		if onWindowsDrive(s) {
			t.Errorf("%q should not be on a Windows drive", s)
		}
	}
}

// Nothing here may read the whole of a process environment into the finding:
// that is where tokens live.
func TestScanWatchersKeepsOnlyTheNameAndDirectory(t *testing.T) {
	fsys := proc("500", "vite", []string{"vite"}, map[string]string{
		"PWD":          "/mnt/c/app",
		"AWS_SECRET":   "super-secret",
		"GITHUB_TOKEN": "ghp_notreal",
	})
	got := scanWatchers(context.Background(), fsys)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(got[0].Name+got[0].Dir, "secret") || strings.Contains(got[0].Name+got[0].Dir, "ghp_") {
		t.Errorf("the finding carries the environment with it: %+v", got[0])
	}
}
