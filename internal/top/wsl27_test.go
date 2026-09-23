package top

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// Captured with `wslkit top --raw` on WSL 2.7.14, the stable release when
// this was written, which has no per-distribution cgroup (#100). Ubuntu runs
// systemd; skrog-engine does not. On 2.7 every distribution shares the VM's
// root cgroup, and Ubuntu's systemd had filled that root with its own groups
// (init.scope, system.slice, user.slice and five .mount units), which both
// distributions could see.
func wsl27(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/wsl-2.7.14-" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWSL27TakesTheProcessMethod(t *testing.T) {
	for _, name := range []string{"systemd", "no-systemd"} {
		s, vm, err := ParseSample(name, wsl27(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Method != MethodProcesses {
			t.Errorf("%s: method %q", name, s.Method)
		}
		if s.MemoryBytes == 0 || s.Processes == 0 || s.AtCsec == 0 {
			t.Errorf("%s: %+v", name, s)
		}
		// What the process method cannot know, it must not claim.
		if s.AnonBytes != nil || s.PeakBytes != nil || s.SwapBytes != nil || s.OOMKills != nil ||
			s.PIDs != nil || s.ReadBytes != nil || s.WriteBytes != nil || s.Pressure != nil {
			t.Errorf("%s: a cgroup-only counter was invented: %+v", name, s)
		}
		// The VM-wide numbers do not need the cgroup and are there on 2.7.
		if vm.TotalBytes == 0 || !vm.HasNet || vm.Pressure == nil || vm.CPUUsec == 0 {
			t.Errorf("%s: vm %+v", name, vm)
		}
	}
}

// The guard that keeps root cgroups out of the report where wsl-user does not
// exist had never met a real 2.7 VM. On this one the root held nine of
// systemd's groups, all of them already inside Ubuntu's resident set. Without
// the guard each would have been a row, and Ubuntu's memory counted twice.
func TestWSL27ReportsNoGroups(t *testing.T) {
	for _, name := range []string{"systemd", "no-systemd"} {
		out := wsl27(t, name)
		if strings.Contains(out, "\ng.") {
			t.Errorf("%s: the script printed group lines on 2.7", name)
		}
		if g := ParseGroups(out); len(g) != 0 {
			t.Errorf("%s: groups %+v", name, g)
		}
	}
}

func TestWSL27RendersWhatItCanAndSaysWhatItCannot(t *testing.T) {
	a, vm, _ := ParseSample("Ubuntu", wsl27(t, "systemd"))
	b, _, _ := ParseSample("skrog-engine", wsl27(t, "no-systemd"))
	var out bytes.Buffer
	Render(&out, Report{VM: vm, Samples: []Sample{a, b}, Interval: 2 * time.Second})
	got := out.String()

	h := header(got)
	for _, col := range []string{"ANON", "READ", "PIDS", "STALL"} {
		if strings.Contains(h, col) {
			t.Errorf("a %s column on a WSL that cannot fill it:\n%s", col, got)
		}
	}
	for _, want := range []string{"PROCESSES", "stalled over the last 10 s"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "groups that belong to none") {
		t.Errorf("group totals on 2.7:\n%s", got)
	}
}
