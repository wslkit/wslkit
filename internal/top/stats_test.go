package top

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Captured on WSL 2.9.12 from a wslc container started with --memory 256M
// --cpus 0.5 and running a busy loop: memory.max 268435456, memory.high max,
// cpu.max "50000 100000".
func TestParseLimitsAsTheKernelWritesThem(t *testing.T) {
	s, err := ParseSession("s", fixtureText(t, "wslc-2.9.12-session-limited.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Containers) != 1 {
		t.Fatalf("containers %+v", s.Containers)
	}
	c := s.Containers[0]
	if c.MemMaxBytes == nil || *c.MemMaxBytes != 256<<20 {
		t.Errorf("memory.max %v", c.MemMaxBytes)
	}
	if c.MemHighBytes != nil {
		t.Errorf("memory.high \"max\" is no limit, got %v", *c.MemHighBytes)
	}
	if c.CPULimit == nil || *c.CPULimit != 0.5 {
		t.Errorf("cpu.max %v", c.CPULimit)
	}
	if c.ThrottledUsec == nil || *c.ThrottledUsec == 0 {
		t.Errorf("throttled %v", c.ThrottledUsec)
	}
	if s.VM.SwapTotalBytes == 0 || s.VM.UptimeSec == 0 {
		t.Errorf("vm swap %d uptime %d", s.VM.SwapTotalBytes, s.VM.UptimeSec)
	}
}

// Captured from skrog-engine with no limits set: memory.high max, cpu.max
// "max 100000"; and df of its root filesystem.
func TestParseNoLimitsAndTheDistributionsDisk(t *testing.T) {
	s, vm, err := ParseSample("skrog-engine", fixtureText(t, "wsl-2.9.12-disk.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if s.MemHighBytes != nil || s.MemMaxBytes != nil || s.CPULimit != nil {
		t.Errorf("\"max\" read as a limit: %v %v %v", s.MemHighBytes, s.MemMaxBytes, s.CPULimit)
	}
	if s.DiskUsedBytes == nil || *s.DiskUsedBytes != 7165908*1024 || s.DiskSizeBytes == nil {
		t.Errorf("disk used %v size %v", s.DiskUsedBytes, s.DiskSizeBytes)
	}
	if vm.SwapTotalBytes != 2097152*1024 {
		t.Errorf("swap %d", vm.SwapTotalBytes)
	}
}

func TestThrottledIsAShareOfTheTime(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	cpu := 0.5
	before := Sample{Distro: "c", Kind: KindWSLC, AtCsec: 100, CPULimit: &cpu, ThrottledUsec: u(0)}
	after := Sample{Distro: "c", Kind: KindWSLC, AtCsec: 300, CPULimit: &cpu, ThrottledUsec: u(1_000_000)}
	r := sampleRates(before, after, time.Second)
	// One second held back over two on the guest's clock.
	if r.Throttled == nil || *r.Throttled < 49.9 || *r.Throttled > 50.1 {
		t.Errorf("throttled %v", r.Throttled)
	}
}

// The limit, throttle and disk columns appear only when some row has
// something to put in them, and a row with no CPU limit is not "0.0%"
// throttled: there is nothing to throttle it.
func TestTheNewColumnsAppearOnlyWhenTheyHaveSomethingToSay(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	cpu := 0.5
	pct := 48.9
	limited := Sample{Distro: "busy", Kind: KindWSLC, MemMaxBytes: u(256 << 20), CPULimit: &cpu, Rates: Rates{Throttled: &pct}}
	// As measured: a container with no CPU limit still has throttled_usec 0 in
	// its cpu.stat, so its rate comes out as 0.0%.
	zero := 0.0
	free := Sample{Distro: "idle", Kind: KindWSLC, ThrottledUsec: u(0), Rates: Rates{Throttled: &zero}}

	plain := rowsTable([]Sample{free}, true, true).String()
	for _, col := range []string{"LIMIT", "THROTTLED", "DISK"} {
		if strings.Contains(plain, col) {
			t.Errorf("a %s column with nothing in it:\n%s", col, plain)
		}
	}

	got := rowsTable([]Sample{limited, free}, true, true).String()
	if !strings.Contains(got, "256.0 MiB, 0.5 CPU") || !strings.Contains(got, "48.9%") {
		t.Errorf("limit or throttle missing:\n%s", got)
	}
	lines := strings.Split(got, "\n")
	col := strings.Index(lines[0], "THROTTLED")
	for _, line := range lines {
		if strings.HasPrefix(line, "idle") {
			if cell := strings.Fields(line[col:])[0]; cell != "-" {
				t.Errorf("a row with no CPU limit shows %q throttled:\n%s", cell, got)
			}
		}
	}

	disk := Sample{Distro: "Ubuntu", Kind: KindDistro, DiskUsedBytes: u(7 << 30), HostDiskBytes: u(9 << 30)}
	if got := rowsTable([]Sample{disk}, true, false).String(); !strings.Contains(got, "DISK USED/FILE") || !strings.Contains(got, "7.0 GiB / 9.0 GiB") {
		t.Errorf("disk column:\n%s", got)
	}
}

func TestTheVMLinesCarryUptimeAndSwap(t *testing.T) {
	var b bytes.Buffer
	renderVM(&b, VM{TotalBytes: 8 << 30, FreeBytes: 6 << 30, CPUs: 4, UptimeSec: 3*3600 + 12*60, CachedBytes: 1 << 30, AnonBytes: 100 << 20, SwapTotalBytes: 2 << 30, SwapFreeBytes: (2 << 30) - (120 << 20)}, nil)
	got := b.String()
	for _, want := range []string{"up 3 h 12 m", "swap 120.0 MiB of 2.0 GiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for sec, want := range map[uint64]string{42: "42 s", 125: "2 m", 90061: "1 d 1 h"} {
		if got := formatUptime(sec); got != want {
			t.Errorf("formatUptime(%d) = %q, want %q", sec, got, want)
		}
	}
}

// wslc containers share the session's storage.vhdx, so the disk is on the
// session's line, not in a column.
func TestTheSessionShowsItsDisk(t *testing.T) {
	n := uint64(3 << 30)
	var b bytes.Buffer
	renderSessions(&b, Report{Sessions: []Session{{Name: "s", DiskBytes: &n, VM: VM{TotalBytes: 1}}}})
	if !strings.Contains(b.String(), "disk: 3.0 GiB, shared by its containers") {
		t.Errorf("got:\n%s", b.String())
	}
}

// wslc names containers at random, so the image is what says what is running.
// It is in the wslc list output top already reads for the names (captured on
// WSL 2.9.12), and takes KIND's place in a table where every row is wslc.
func TestContainersShowTheirImage(t *testing.T) {
	out, list := sessionFixture(t)
	s, err := ParseSession("s", out, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Containers {
		if c.Image != "busybox" {
			t.Errorf("%s: image %q", c.Distro, c.Image)
		}
	}
	got := rowsTable(s.Containers, true, false).String()
	if h := strings.Fields(strings.SplitN(got, "\n", 2)[0]); len(h) < 2 || h[1] != "IMAGE" || strings.Contains(got, " wslc ") {
		t.Errorf("the wslc table does not name the image:\n%s", got)
	}
	d, _, _ := ParseSample("skrog-engine", realOutput(t))
	if h := strings.Fields(strings.SplitN(rowsTable([]Sample{d}, true, false).String(), "\n", 2)[0]); h[1] != "KIND" {
		t.Errorf("the distribution table lost KIND: %v", h)
	}
	if o := sampleJSON(s.Containers[0], "name"); o["image"] != "busybox" {
		t.Errorf("JSON image %v", o["image"])
	}
}

func TestShortImageKeepsTheEnd(t *testing.T) {
	for in, want := range map[string]string{
		"busybox":     "busybox",
		"alpine:3.20": "alpine:3.20",
		"":            "-",
		"mcr.microsoft.com/devcontainers/base:ubuntu-24.04": "…vcontainers/base:ubuntu-24.04", // 30 runes, the column's width
	} {
		if got := shortImage(in); got != want {
			t.Errorf("shortImage(%q) = %q, want %q", in, got, want)
		}
	}
}
