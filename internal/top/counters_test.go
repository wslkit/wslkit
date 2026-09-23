package top

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// Captured on WSL 2.9.12 from skrog-engine, a distribution without systemd
// that runs Docker Engine with the cgroupfs driver, so /docker sits at the
// VM's root beside wsl-user.
func realOutput(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/wsl-2.9.12-no-systemd.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseReadsEveryCgroupCounter(t *testing.T) {
	s, vm, err := ParseSample("skrog-engine", realOutput(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.Method != MethodCgroup || s.CgroupPath != "/wsl-user/distro-111" {
		t.Fatalf("method %q path %q", s.Method, s.CgroupPath)
	}
	for name, c := range map[string]struct {
		got  *uint64
		want uint64
	}{
		"peak":  {s.PeakBytes, 6475821056},
		"swap":  {s.SwapBytes, 0},
		"pids":  {s.PIDs, 90},
		"oom":   {s.OOMKills, 0},
		"read":  {s.ReadBytes, 8085590016},
		"write": {s.WriteBytes, 1571708928},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s: got %v, want %d", name, c.got, c.want)
		}
	}
	if s.Pressure == nil || s.Pressure.IO != 0.91 {
		t.Errorf("pressure %+v", s.Pressure)
	}
	if s.AtCsec != 503221 {
		t.Errorf("clock %d", s.AtCsec)
	}
	if !vm.HasNet || vm.NetRxBytes != 167349970 || vm.NetTxBytes != 349484 {
		t.Errorf("net %+v", vm)
	}
	// 10373 ticks at 100 Hz.
	if vm.CPUUsec != 103_730_000 {
		t.Errorf("vm cpu %d", vm.CPUUsec)
	}
	if vm.Pressure == nil || vm.Pressure.IO != 3.14 || vm.Pressure.CPU != 0.89 {
		t.Errorf("vm pressure %+v", vm.Pressure)
	}
}

// Without a cgroup these counters do not exist, and a missing number must not
// turn into a zero that reads as "idle" or "never ran out of memory".
func TestMissingCountersStayUnknown(t *testing.T) {
	s, vm, err := ParseSample("old", processOut)
	if err != nil {
		t.Fatal(err)
	}
	if s.PeakBytes != nil || s.SwapBytes != nil || s.OOMKills != nil || s.PIDs != nil ||
		s.ReadBytes != nil || s.WriteBytes != nil || s.Pressure != nil {
		t.Errorf("the process method invented a counter: %+v", s)
	}
	if vm.HasNet || vm.Pressure != nil {
		t.Errorf("the VM invented a counter: %+v", vm)
	}
	if g := ParseGroups(processOut); len(g) != 0 {
		t.Errorf("groups without a cgroup: %+v", g)
	}
}

func TestParseGroupsFindsWSLAndDocker(t *testing.T) {
	groups := ParseGroups(realOutput(t))
	if len(groups) != 2 {
		t.Fatalf("got %+v", groups)
	}
	byName := map[string]Sample{}
	for _, g := range groups {
		byName[g.Distro] = g
	}
	wsl, docker := byName["wsl"], byName["docker"]
	if wsl.Kind != KindWSL || wsl.CgroupPath != "/wsl-user/non-distro" || wsl.MemoryBytes != 970752 {
		t.Errorf("wsl %+v", wsl)
	}
	if docker.Kind != KindCgroup || docker.CgroupPath != "/docker" || docker.MemoryBytes != 17551360 {
		t.Errorf("docker %+v", docker)
	}
	if docker.PIDs == nil || *docker.PIDs != 6 {
		t.Errorf("docker pids %v", docker.PIDs)
	}
}

// A cgroup name can carry a dot; the keys never do.
func TestParseGroupsKeepsADottedName(t *testing.T) {
	g := ParseGroups("g.system.slice.memory_bytes=42\ng.system.slice.pids=3\n")
	if len(g) != 1 || g[0].Distro != "system.slice" || g[0].MemoryBytes != 42 {
		t.Fatalf("got %+v", g)
	}
}

// The rate divides by the guest's own clock, not by the interval that was
// asked for: the sweep between the two samples takes time too. Here the
// samples are 2.19 s apart on the guest's clock, and 2.19 s of CPU is one
// processor, not the 109.5% that dividing by the requested two seconds gives.
func TestRatesUseTheGuestClock(t *testing.T) {
	before := Sample{CPUUsec: 1_000_000, AtCsec: 100_000}
	after := Sample{CPUUsec: 3_190_000, AtCsec: 100_219}
	got := CPUPercent(before, after, 2*time.Second)
	if got == nil || *got < 99.9 || *got > 100.1 {
		t.Fatalf("got %v, want 100", got)
	}
}

func TestWithRatesFillsEveryRate(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	before := Report{
		VM:      VM{TotalBytes: 1, AtCsec: 1000, HasNet: true},
		Samples: []Sample{{Distro: "Ubuntu", Kind: KindDistro, AtCsec: 1000, ReadBytes: u(0), WriteBytes: u(100)}},
		Groups:  []Sample{{Distro: "docker", Kind: KindCgroup, AtCsec: 1000, ReadBytes: u(0), WriteBytes: u(0)}},
	}
	after := Report{
		VM: VM{TotalBytes: 1, CPUUsec: 4_000_000, AtCsec: 1200, HasNet: true, NetRxBytes: 2048, NetTxBytes: 1024},
		Samples: []Sample{
			{Distro: "Ubuntu", Kind: KindDistro, AtCsec: 1200, CPUUsec: 1_000_000, ReadBytes: u(4096), WriteBytes: u(50)},
			{Distro: "new", Kind: KindDistro, AtCsec: 1200, CPUUsec: 1_000_000},
		},
		Groups: []Sample{{Distro: "docker", Kind: KindCgroup, AtCsec: 1200, CPUUsec: 2_000_000, ReadBytes: u(0), WriteBytes: u(2000)}},
	}
	r := WithRates(before, after, time.Second)

	near := func(name string, p *float64, want float64) {
		t.Helper()
		if p == nil || *p < want-0.01 || *p > want+0.01 {
			t.Errorf("%s: got %v, want %v", name, p, want)
		}
	}
	// Two seconds on the guest's clock, although one was asked for.
	ub := r.Samples[0]
	near("ubuntu cpu", ub.Rates.CPU, 50)
	near("ubuntu read", ub.Rates.Read, 2048)
	// A counter that went backwards is a restart, not a negative rate.
	if ub.Rates.Write != nil {
		t.Errorf("write went backwards and should have no rate: %v", *ub.Rates.Write)
	}
	// A row that was not in the first sample has nothing to compare with.
	if r.Samples[1].Rates.CPU != nil {
		t.Error("a new row cannot have a rate")
	}
	near("docker cpu", r.Groups[0].Rates.CPU, 100)
	near("docker write", r.Groups[0].Rates.Write, 1000)
	// Four seconds of CPU across the VM in two seconds is two processors.
	near("vm cpu", r.VM.Rates.CPU, 200)
	near("vm rx", r.VM.Rates.NetRx, 1024)
	near("vm tx", r.VM.Rates.NetTx, 512)
}

func TestRenderShowsGroupsAndCgroupColumns(t *testing.T) {
	out := realOutput(t)
	s, vm, _ := ParseSample("skrog-engine", out)
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{s}, Groups: ParseGroups(out)})
	got := b.String()
	for _, want := range []string{"docker", "cgroup", "WSL itself", "PIDS", "STALL", groupsNote, "stalled over the last 10 s", "(reclaimable)", "(unreclaimable)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// Nothing has swapped, so there is no column for it.
	if strings.Contains(header(got), "SWAP") {
		t.Errorf("an all-zero swap column:\n%s", got)
	}
	// No interval, so no rate columns.
	if h := header(got); strings.Contains(h, "READ") || strings.Contains(h, "CPU") {
		t.Errorf("rate columns without rates:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimRight(line, " ") != line {
			t.Errorf("line has trailing whitespace: %q", line)
		}
	}
}

// On a WSL without the per-distribution cgroup the columns that need it are
// left out, and the report says why, rather than showing dashes nobody can
// explain.
func TestRenderWithoutCgroupsSaysWhatIsMissing(t *testing.T) {
	s, vm, _ := ParseSample("old", processOut)
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{s}, Interval: time.Second})
	got := b.String()
	if h := header(got); strings.Contains(h, "READ") || strings.Contains(h, "PIDS") || strings.Contains(h, "STALL") {
		t.Errorf("cgroup columns without a cgroup:\n%s", got)
	}
	if !strings.Contains(got, upperFirst(cgroupOnlyNote)) {
		t.Errorf("the report does not say why:\n%s", got)
	}
}

func TestRenderNamesOOMKills(t *testing.T) {
	s, vm, _ := ParseSample("skrog-engine", strings.Replace(realOutput(t), "\noom_kills=0", "\noom_kills=3", 1))
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{s}})
	if !strings.Contains(b.String(), "skrog-engine: the kernel has killed 3 process(es)") {
		t.Errorf("got:\n%s", b.String())
	}
}

// The JSON schema is additive only (ADR 0006): distributions stay a list of
// distributions, and the groups get a list of their own.
func TestJSONKeepsDistributionsAndAddsGroups(t *testing.T) {
	out := realOutput(t)
	s, vm, _ := ParseSample("skrog-engine", out)
	o := JSON(Report{VM: vm, Samples: []Sample{s}, Groups: ParseGroups(out)})
	distros := o["distributions"].([]map[string]any)
	if len(distros) != 1 || distros[0]["distribution"] != "skrog-engine" {
		t.Fatalf("distributions %+v", distros)
	}
	if distros[0]["read_bytes"] != uint64(8085590016) || distros[0]["pids"] != uint64(90) {
		t.Errorf("counters %+v", distros[0])
	}
	groups := o["groups"].([]map[string]any)
	if len(groups) != 2 || groups[0]["name"] != "docker" || groups[0]["kind"] != "cgroup" {
		t.Errorf("groups %+v", groups)
	}
	vmOut := o["vm"].(map[string]any)
	if vmOut["net_rx_bytes"] != uint64(167349970) {
		t.Errorf("vm %+v", vmOut)
	}
	if o["attributed_bytes"] != uint64(208789504) {
		t.Errorf("attributed %v: groups must not be counted as distributions", o["attributed_bytes"])
	}
}

// On screen WSL's own cgroup is "WSL itself"; in the JSON it keeps the name
// `wsl`, so nothing that reads it has to change.
func TestTheWSLRowKeepsItsNameInJSON(t *testing.T) {
	out := realOutput(t)
	s, vm, _ := ParseSample("skrog-engine", out)
	o := JSON(Report{VM: vm, Samples: []Sample{s}, Groups: ParseGroups(out)})
	var names []string
	for _, g := range o["groups"].([]map[string]any) {
		names = append(names, g["name"].(string))
	}
	if !strings.Contains(strings.Join(names, ","), "wsl") {
		t.Errorf("groups %v", names)
	}
	if displayName(Sample{Distro: "wsl", Kind: KindWSL}) != "WSL itself" || displayName(Sample{Distro: "Ubuntu", Kind: KindDistro}) != "Ubuntu" {
		t.Error("display names")
	}
}
