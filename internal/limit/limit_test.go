package limit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The order is the whole safety property: a distribution already over the new
// ceiling must start being throttled before it starts being killed. Writing
// memory.max first would OOM-kill whatever was running in the gap.
func TestThrottleIsWrittenBeforeTheCeiling(t *testing.T) {
	writes := Plan(Limits{MemoryMax: 4 << 30, MemoryHigh: 3 << 30})
	if len(writes) != 2 {
		t.Fatalf("writes = %+v", writes)
	}
	if writes[0].File != "memory.high" || writes[1].File != "memory.max" {
		t.Errorf("order = %s, %s", writes[0].File, writes[1].File)
	}
}

func TestPlanRendersCPUAsQuotaAndPeriod(t *testing.T) {
	writes := Plan(Limits{CPUs: 1.5})
	if len(writes) != 1 || writes[0].File != "cpu.max" {
		t.Fatalf("writes = %+v", writes)
	}
	if writes[0].Value != "150000 100000" {
		t.Errorf("cpu.max = %q, want a quota of 150000 in a 100000 window", writes[0].Value)
	}
}

// A limit so small the distribution cannot run at all is worse than none: it
// hangs the machine rather than slowing it.
func TestTinyCPULimitIsFloored(t *testing.T) {
	writes := Plan(Limits{CPUs: 0.000001})
	if !strings.HasPrefix(writes[0].Value, "1000 ") {
		t.Errorf("cpu.max = %q, want the floor", writes[0].Value)
	}
}

func TestSwapOffAndSwapCeiling(t *testing.T) {
	if w := Plan(Limits{SwapOff: true}); len(w) != 1 || w[0].Value != "0" {
		t.Errorf("--no-swap = %+v", w)
	}
	if w := Plan(Limits{Swap: 1 << 30}); len(w) != 1 || w[0].Value != "1073741824" {
		t.Errorf("--swap = %+v", w)
	}
}

func TestClearUsesTheKernelsWordForUnlimited(t *testing.T) {
	got := map[string]string{}
	for _, w := range Clear() {
		got[w.File] = w.Value
	}
	for file, want := range map[string]string{
		"memory.max":      "max",
		"memory.high":     "max",
		"memory.swap.max": "max",
		"cpu.max":         "max 100000",
	} {
		if got[file] != want {
			t.Errorf("%s = %q, want %q", file, got[file], want)
		}
	}
}

// The check that stops a typo from killing somebody's build. The kernel accepts
// a ceiling below current usage and then kills processes until the figure fits.
func TestCeilingBelowCurrentUsageIsRefused(t *testing.T) {
	c := Current{Node: "/wsl-user/distro-1", MemoryCurrent: 2 << 30}
	err := Limits{MemoryMax: 100 << 20}.CheckAgainstUsage(c)
	if err == nil {
		t.Fatal("a ceiling below what is in use must be refused")
	}
	if !strings.Contains(err.Error(), "killing processes") {
		t.Errorf("the error should say what would happen: %v", err)
	}
	// Above current usage is fine, and so is a limit that names no ceiling.
	if err := (Limits{MemoryMax: 4 << 30}).CheckAgainstUsage(c); err != nil {
		t.Errorf("4 GiB over 2 GiB in use: %v", err)
	}
	if err := (Limits{CPUs: 2}).CheckAgainstUsage(c); err != nil {
		t.Errorf("a CPU limit cannot OOM anything: %v", err)
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[string]string{
		"max":        "unlimited",
		"":           "unlimited",
		"1073741824": "1.0 GiB",
	} {
		if got := FormatLimit(in); got != want {
			t.Errorf("FormatLimit(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"max 100000":    "unlimited",
		"100000 100000": "1 processor",
		"200000 100000": "2 processors",
		"150000 100000": "1.5 processors",
	} {
		if got := FormatCPUMax(in); got != want {
			t.Errorf("FormatCPUMax(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeDistro answers the read script and records what was written.
type fakeDistro struct {
	node    string
	values  map[string]string
	current string
	written []string
	noNode  bool
}

func (f *fakeDistro) Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error) {
	if strings.Contains(script, "echo \"node=$node\"") || strings.Contains(script, "memory_current=") {
		if f.noNode {
			return "node=\n", nil
		}
		return strings.Join([]string{
			"node=" + f.node,
			"memory_max=" + f.values["memory.max"],
			"memory_high=" + f.values["memory.high"],
			"swap_max=" + f.values["memory.swap.max"],
			"cpu_max=" + f.values["cpu.max"],
			"memory_current=" + f.current,
			"systemd=no",
			"",
		}, "\n"), nil
	}
	// The write script: record each redirect in order.
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "printf ") {
			f.written = append(f.written, line)
		}
	}
	return "applied\n", nil
}

func TestReadParsesTheCgroup(t *testing.T) {
	f := &fakeDistro{
		node:    "/wsl-user/distro-42",
		current: "1073741824",
		values:  map[string]string{"memory.max": "max", "memory.high": "2147483648", "memory.swap.max": "max", "cpu.max": "150000 100000"},
	}
	c, err := Read(context.Background(), f, "Ubuntu", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c.Node != "/wsl-user/distro-42" || c.MemoryCurrent != 1<<30 || c.MemoryHigh != "2147483648" {
		t.Fatalf("current = %+v", c)
	}
}

// A runtime without per-distribution cgroups has nowhere to put a limit, and
// saying which runtimes have them is more use than "not found".
func TestNoCgroupIsNamedAndExplained(t *testing.T) {
	f := &fakeDistro{noNode: true}
	_, err := Read(context.Background(), f, "Ubuntu", time.Second)
	var none ErrNoCgroup
	if !errors.As(err, &none) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "2.9.11") {
		t.Errorf("the error should say where the feature arrived: %v", err)
	}
}

// The node is resolved inside the writing shell, not passed in, because it is
// named after the distribution's init pid and changes on every restart.
func TestWriteScriptResolvesTheNodeItself(t *testing.T) {
	s := WriteScript(Plan(Limits{MemoryMax: 1 << 30}))
	if !strings.Contains(s, "/proc/self/cgroup") {
		t.Error("the write script must find the node itself")
	}
	if !strings.Contains(s, "|| {") {
		t.Error("a failed write must fail the script rather than pass silently")
	}
	if strings.Contains(s, "distro-42") {
		t.Error("no node may be baked into the script")
	}
}

func TestApplyReportsAnUnconfirmedWrite(t *testing.T) {
	silent := runnerFunc(func(ctx context.Context, distro, script string, timeout time.Duration) (string, error) {
		return "", nil // no "applied"
	})
	err := Apply(context.Background(), silent, "Ubuntu", Plan(Limits{MemoryMax: 1 << 30}), time.Second)
	if err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("err = %v", err)
	}
}

type runnerFunc func(ctx context.Context, distro, script string, timeout time.Duration) (string, error)

func (f runnerFunc) Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error) {
	return f(ctx, distro, script, timeout)
}
