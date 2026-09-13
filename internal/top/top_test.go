package top

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const cgroupOut = `method=cgroup
cgroup_path=/wsl-user/distro-42
memory_bytes=1073741824
anon_bytes=536870912
cpu_usec=5000000
processes=56
init=systemd
mem_total_kb=8169548
mem_free_kb=6000000
mem_available_kb=7000000
mem_cached_kb=1100000
mem_anon_kb=140000
cpus=4
uptime_sec=1234
`

const processOut = `method=processes
memory_kb=449024
cpu_ticks=500
clock_hz=100
processes=14
init=init(skrog)
mem_total_kb=8169548
mem_free_kb=6000000
mem_available_kb=7000000
mem_cached_kb=1100000
mem_anon_kb=140000
cpus=4
uptime_sec=1234
`

func TestParseCgroupSample(t *testing.T) {
	s, vm, err := ParseSample("Ubuntu", cgroupOut)
	if err != nil {
		t.Fatal(err)
	}
	if s.Method != MethodCgroup {
		t.Errorf("method %q", s.Method)
	}
	if s.MemoryBytes != 1<<30 {
		t.Errorf("memory %d", s.MemoryBytes)
	}
	if s.AnonBytes == nil || *s.AnonBytes != 512<<20 {
		t.Errorf("anon %v", s.AnonBytes)
	}
	if s.CPUUsec != 5_000_000 {
		t.Errorf("cpu %d", s.CPUUsec)
	}
	if s.CgroupPath != "/wsl-user/distro-42" {
		t.Errorf("path %q", s.CgroupPath)
	}
	if vm.TotalBytes != 8169548*1024 || vm.CPUs != 4 {
		t.Errorf("vm %+v", vm)
	}
}

// Clock ticks are not guaranteed to be hundredths of a second, so the
// distribution reports its own rate and the conversion uses it.
func TestParseProcessSampleUsesTheReportedClockRate(t *testing.T) {
	s, _, err := ParseSample("skrog", processOut)
	if err != nil {
		t.Fatal(err)
	}
	if s.Method != MethodProcesses {
		t.Errorf("method %q", s.Method)
	}
	if s.MemoryBytes != 449024*1024 {
		t.Errorf("memory %d", s.MemoryBytes)
	}
	// 500 ticks at 100 Hz is five seconds.
	if s.CPUUsec != 5_000_000 {
		t.Errorf("cpu %d, want 5000000", s.CPUUsec)
	}
	if s.AnonBytes != nil {
		t.Error("the process method cannot report anonymous memory and must not pretend to")
	}

	at250 := strings.Replace(processOut, "clock_hz=100", "clock_hz=250", 1)
	s2, _, err := ParseSample("skrog", at250)
	if err != nil {
		t.Fatal(err)
	}
	if s2.CPUUsec != 2_000_000 {
		t.Errorf("at 250 Hz, 500 ticks is two seconds, got %d", s2.CPUUsec)
	}
}

func TestParseSampleRejectsWhatItCannotUnderstand(t *testing.T) {
	if _, _, err := ParseSample("X", ""); err == nil {
		t.Error("empty output should be an error")
	}
	// Without the method there is no way to know what the numbers mean.
	if _, _, err := ParseSample("X", "processes=3\ninit=systemd\n"); err == nil {
		t.Error("output with no method should be an error")
	}
}

func TestCPUPercent(t *testing.T) {
	before := Sample{CPUUsec: 1_000_000}
	after := Sample{CPUUsec: 4_000_000}
	// Three seconds of CPU over two seconds of wall clock is one and a half
	// processors.
	got := CPUPercent(before, after, 2*time.Second)
	if got == nil || *got < 149.9 || *got > 150.1 {
		t.Fatalf("got %v, want about 150", got)
	}
	if CPUPercent(before, after, 0) != nil {
		t.Error("a rate needs an interval")
	}
	// A counter that went backwards means the distribution restarted; report
	// nothing rather than a negative or an enormous number.
	if CPUPercent(after, before, time.Second) != nil {
		t.Error("a counter going backwards should report nothing")
	}
}

func TestUsedBytesNeverUnderflows(t *testing.T) {
	if got := (VM{TotalBytes: 10, FreeBytes: 20}).UsedBytes(); got != 0 {
		t.Errorf("got %d", got)
	}
}

// ---------------------------------------------------------------- collect

type fakeRunner struct {
	running []string
	out     map[string]string
	err     map[string]error
	calls   int
	// second is what to return on the second sweep, so a rate can be tested.
	second map[string]string
}

func (f *fakeRunner) Running(ctx context.Context) ([]string, error) {
	return f.running, nil
}

func (f *fakeRunner) Sample(ctx context.Context, distro string, timeout time.Duration) (string, error) {
	f.calls++
	if e, ok := f.err[distro]; ok {
		return "", e
	}
	if f.second != nil && f.calls > len(f.running) {
		if s, ok := f.second[distro]; ok {
			return s, nil
		}
	}
	return f.out[distro], nil
}

func TestCollectMeasuresEveryRunningDistribution(t *testing.T) {
	r := &fakeRunner{
		running: []string{"Ubuntu", "skrog"},
		out:     map[string]string{"Ubuntu": cgroupOut, "skrog": processOut},
	}
	report, rates, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 2 {
		t.Fatalf("got %d samples", len(report.Samples))
	}
	if rates != nil {
		t.Error("one sample cannot produce a rate")
	}
	if report.VM.TotalBytes == 0 {
		t.Error("the VM should have been reported")
	}
}

// One distribution failing must not hide the others: a distribution shutting
// down is exactly when someone runs this.
func TestCollectReportsOneFailureWithoutLosingTheRest(t *testing.T) {
	r := &fakeRunner{
		running: []string{"Ubuntu", "broken"},
		out:     map[string]string{"Ubuntu": cgroupOut},
		err:     map[string]error{"broken": errors.New("the distribution is shutting down")},
	}
	report, _, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatalf("the sweep should not fail: %v", err)
	}
	if len(report.Samples) != 2 {
		t.Fatalf("both should be reported: %+v", report.Samples)
	}
	sorted := Sorted(report.Samples)
	// The one that failed sorts last, so it does not head the table.
	if sorted[len(sorted)-1].Err == nil {
		t.Errorf("the failure should sort last: %+v", sorted)
	}
}

func TestCollectComputesARateFromTwoSweeps(t *testing.T) {
	busier := strings.Replace(cgroupOut, "cpu_usec=5000000", "cpu_usec=6000000", 1)
	r := &fakeRunner{
		running: []string{"Ubuntu"},
		out:     map[string]string{"Ubuntu": cgroupOut},
		second:  map[string]string{"Ubuntu": busier},
	}
	// A short interval keeps the test fast; the arithmetic is the point.
	report, rates, err := Collect(context.Background(), r, Options{Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if report.Interval != 10*time.Millisecond {
		t.Errorf("interval %v", report.Interval)
	}
	p := rates["Ubuntu"]
	if p == nil {
		t.Fatal("no rate was computed")
	}
	// One second of CPU in ten milliseconds is a hundred processors' worth;
	// the number is absurd but the arithmetic is what is under test.
	if *p < 9999 || *p > 10001 {
		t.Errorf("got %v", *p)
	}
}

func TestCollectOnlyLimitsWhatIsMeasured(t *testing.T) {
	r := &fakeRunner{
		running: []string{"Ubuntu", "skrog"},
		out:     map[string]string{"Ubuntu": cgroupOut, "skrog": processOut},
	}
	report, _, err := Collect(context.Background(), r, Options{Only: []string{"skrog"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 1 || report.Samples[0].Distro != "skrog" {
		t.Fatalf("got %+v", report.Samples)
	}
	// And a name that is not running is simply not measured.
	report2, _, _ := Collect(context.Background(), r, Options{Only: []string{"nope"}})
	if len(report2.Samples) != 0 {
		t.Errorf("got %+v", report2.Samples)
	}
}

func TestCollectWithNothingRunning(t *testing.T) {
	report, rates, err := Collect(context.Background(), &fakeRunner{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 0 || rates != nil {
		t.Errorf("got %+v %v", report.Samples, rates)
	}
}

// ---------------------------------------------------------------- render

func TestRenderSaysHowItMeasuredAndWhatIsMissing(t *testing.T) {
	s, vm, _ := ParseSample("skrog", processOut)
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{s}}, nil)
	out := b.String()

	if !strings.Contains(out, "utility VM:") {
		t.Errorf("the VM total is the headline and is missing:\n%s", out)
	}
	// The distinction that explains why vmmem stays large.
	if !strings.Contains(out, "page cache") || !strings.Contains(out, "anonymous") {
		t.Errorf("the reclaimable split is missing:\n%s", out)
	}
	// The columns do not add up, and the report has to say why.
	if !strings.Contains(out, processesNote) {
		t.Errorf("the method note is missing:\n%s", out)
	}
	if !strings.Contains(out, "attributed to distributions") {
		t.Errorf("the attributed total is missing:\n%s", out)
	}
}

func TestRenderUsesTheCgroupNoteWhenThatIsWhatHappened(t *testing.T) {
	s, vm, _ := ParseSample("Ubuntu", cgroupOut)
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{s}}, nil)
	if !strings.Contains(b.String(), cgroupNote) {
		t.Errorf("got:\n%s", b.String())
	}
}

// A CPU column only appears when a rate was actually measured, rather than
// showing a dash that reads as "idle".
func TestRenderOmitsTheCPUColumnWithoutRates(t *testing.T) {
	s, vm, _ := ParseSample("Ubuntu", cgroupOut)
	var without bytes.Buffer
	Render(&without, Report{VM: vm, Samples: []Sample{s}}, nil)
	if strings.Contains(without.String(), "CPU") {
		t.Errorf("no rate was measured, so there should be no CPU column:\n%s", without.String())
	}

	pct := 42.0
	var with bytes.Buffer
	Render(&with, Report{VM: vm, Samples: []Sample{s}}, map[string]*float64{"Ubuntu": &pct})
	if !strings.Contains(with.String(), "42.0%") {
		t.Errorf("the rate is missing:\n%s", with.String())
	}
}

func TestRenderWithNothingRunning(t *testing.T) {
	var b bytes.Buffer
	Render(&b, Report{}, nil)
	if !strings.Contains(b.String(), "no distributions are running") {
		t.Errorf("got %q", b.String())
	}
}

func TestRenderHasNoTrailingWhitespace(t *testing.T) {
	a, vm, _ := ParseSample("Ubuntu", cgroupOut)
	c, _, _ := ParseSample("skrog", processOut)
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{a, c}}, nil)
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.TrimRight(line, " ") != line {
			t.Errorf("line has trailing whitespace: %q", line)
		}
	}
}

func TestJSONCarriesTheMethodAndItsCaveat(t *testing.T) {
	s, vm, _ := ParseSample("skrog", processOut)
	o := JSON(Report{VM: vm, Samples: []Sample{s}}, nil)
	if o["method"] != string(MethodProcesses) {
		t.Errorf("method %v", o["method"])
	}
	if o["note"] != processesNote {
		t.Errorf("a consumer should not have to guess what the numbers mean: %v", o["note"])
	}
	if o["attributed_bytes"] != uint64(449024*1024) {
		t.Errorf("attributed %v", o["attributed_bytes"])
	}
	vmOut, ok := o["vm"].(map[string]any)
	if !ok {
		t.Fatalf("vm is %T", o["vm"])
	}
	if vmOut["used_bytes"] != vm.UsedBytes() {
		t.Errorf("used %v", vmOut["used_bytes"])
	}
}

// A report where one distribution used a cgroup and another did not cannot
// happen on one VM, and claiming a single method would be a lie if it did.
func TestReportMethodIsEmptyWhenTheSamplesDisagree(t *testing.T) {
	a, _, _ := ParseSample("Ubuntu", cgroupOut)
	b, _, _ := ParseSample("skrog", processOut)
	if got := (Report{Samples: []Sample{a, b}}).Method(); got != "" {
		t.Errorf("got %q", got)
	}
	if MethodNote("") != "" {
		t.Error("there is no note for a method that was not agreed")
	}
}

func TestFormatSizeTruncates(t *testing.T) {
	for _, c := range []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1<<30 - 1, "1023.9 MiB"},
		{1 << 30, "1.0 GiB"},
	} {
		if got := FormatSize(c.in); got != c.want {
			t.Errorf("FormatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The script runs through a shell inside the guest, where a carriage return
// turns a line into a command that does not exist.
func TestSampleScriptHasUnixLineEndings(t *testing.T) {
	if strings.Contains(SampleScript, "\r") {
		t.Fatal("the sample script contains a carriage return, which the guest shell will choke on")
	}
	if !strings.HasSuffix(SampleScript, "\n") {
		t.Error("the script should end with a newline")
	}
}
