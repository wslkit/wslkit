package top

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Captured on WSL 2.9.12: SessionScript run through `wslc system session run`
// with two busybox containers up, one of them spinning, and `wslc list
// --format json` for the same session.
func sessionFixture(t *testing.T) (string, string) {
	t.Helper()
	out, err := os.ReadFile("testdata/wslc-2.9.12-session.txt")
	if err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile("testdata/wslc-2.9.12-list.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(list)
}

func TestParseSessionNamesEachContainer(t *testing.T) {
	out, list := sessionFixture(t)
	s, err := ParseSession("wslc-cli-user", out, list)
	if err != nil {
		t.Fatal(err)
	}
	if s.VM.TotalBytes == 0 || s.VM.AtCsec == 0 || s.VM.Pressure == nil {
		t.Errorf("vm %+v", s.VM)
	}
	names := map[string]Sample{}
	for _, c := range s.Containers {
		names[c.Distro] = c
	}
	for _, want := range []string{"wk-spike-busy", "wk-spike-idle"} {
		c, ok := names[want]
		if !ok {
			t.Fatalf("no %s in %+v", want, s.Containers)
		}
		if c.Kind != KindWSLC || !strings.HasPrefix(c.CgroupPath, "/docker/") || c.PIDs == nil || c.MemoryBytes == 0 {
			t.Errorf("%s: %+v", want, c)
		}
	}
}

// Without names, a container is still a row: shown by its ID, not dropped.
func TestParseSessionWithoutNamesKeepsTheRows(t *testing.T) {
	out, _ := sessionFixture(t)
	s, err := ParseSession("x", out, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Containers) != 2 {
		t.Fatalf("got %+v", s.Containers)
	}
	for _, c := range s.Containers {
		if len(c.Distro) != 12 {
			t.Errorf("expected a short ID, got %q", c.Distro)
		}
	}
}

// Right after the VM boots, before any container has run, there is no
// /docker at all: that is a session with no containers, not an error.
func TestParseSessionWithNoContainers(t *testing.T) {
	out, _ := sessionFixture(t)
	var vmOnly []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "g.") {
			vmOnly = append(vmOnly, line)
		}
	}
	s, err := ParseSession("x", strings.Join(vmOnly, "\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Containers) != 0 || s.VM.TotalBytes == 0 {
		t.Errorf("got %+v", s)
	}
}

// sessionRunner is a fakeRunner that also has wslc sessions.
type sessionRunner struct {
	fakeRunner
	sessions   []string
	out, list  string
	sessionErr error
	sampled    int
	vms        []HostVM
}

func (s *sessionRunner) Sessions(ctx context.Context) ([]string, error) {
	return s.sessions, s.sessionErr
}

func (s *sessionRunner) SampleSession(ctx context.Context, name string, timeout time.Duration) (string, error) {
	s.sampled++
	return s.out, nil
}

func (s *sessionRunner) ContainerNames(ctx context.Context, name string) (string, error) {
	return s.list, nil
}

func (s *sessionRunner) HostVMs(ctx context.Context) ([]HostVM, error) { return s.vms, nil }

// top asks wslc about exactly the sessions the reader reports running, and
// nothing else. The production reader returns only sessions whose VM is up
// (wslcsess); with none, wslc is not asked anything and there is no section.
func TestSessionsAreSampledOnlyWhenRunning(t *testing.T) {
	out, list := sessionFixture(t)
	r := &sessionRunner{out: out, list: list}
	r.running = []string{"skrog-engine"}
	r.fakeRunner.out = map[string]string{"skrog-engine": realOutput(t)}

	report, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.sampled != 0 || report.Sessions != nil {
		t.Fatalf("wslc was asked with no session running: %d calls, %+v", r.sampled, report.Sessions)
	}
	if _, ok := JSON(report)["wslc_sessions"]; ok {
		t.Error("JSON has a wslc section with no session running")
	}
	var b bytes.Buffer
	Render(&b, report)
	if strings.Contains(b.String(), "wslc session") {
		t.Errorf("a wslc section with no session running:\n%s", b.String())
	}

	r.sessions = []string{"s"}
	report, err = Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.sampled != 1 || len(report.Sessions) != 1 || len(report.Sessions[0].Containers) != 2 {
		t.Fatalf("sessions %+v after %d calls", report.Sessions, r.sampled)
	}
}

// A session VM holds memory with no distribution running, and top must still
// show it.
func TestSessionsWithNoDistributionRunning(t *testing.T) {
	out, list := sessionFixture(t)
	r := &sessionRunner{sessions: []string{"s"}, out: out, list: list}
	report, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	Render(&b, report)
	for _, want := range []string{"no distributions are running", "wslc session s (preview)", "wk-spike-busy"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q:\n%s", want, b.String())
		}
	}
	if JSON(report)["wslc_sessions"] == nil {
		t.Error("the empty-distribution JSON dropped the sessions")
	}
}

// With two VMs on screen, explanations printed between them made one run into
// the next. Each VM is its own section under a rule, and everything that
// explains comes once, after the last section.
func TestEachVMIsASectionAndTheNotesComeLast(t *testing.T) {
	out, list := sessionFixture(t)
	s, _ := ParseSession("s", out, list)
	d, vm, _ := ParseSample("skrog-engine", realOutput(t))
	var b bytes.Buffer
	Render(&b, Report{VM: vm, Samples: []Sample{d}, Groups: ParseGroups(realOutput(t)), Sessions: []Session{s}})
	got := b.String()

	utility := strings.Index(got, rule("utility VM"))
	session := strings.Index(got, rule("wslc session s (preview)"))
	notes := strings.Index(got, rule("notes"))
	if utility != 0 || session <= utility || notes <= session {
		t.Fatalf("sections out of order (utility %d, session %d, notes %d):\n%s", utility, session, notes, got)
	}
	for _, explanation := range []string{groupsNote, sessionsNote, upperFirst(cgroupNote)} {
		if i := strings.Index(got, explanation); i < notes {
			t.Errorf("%q is not in the notes at the end:\n%s", explanation, got)
		}
	}
}

// The reader errs only when wslc is there and its sessions could not be
// listed. The distributions are still reported, the wslc section is empty,
// and a note says why.
func TestSessionListFailureIsANoteNotAnError(t *testing.T) {
	r := &sessionRunner{sessionErr: errors.New("access is denied")}
	r.running = []string{"skrog-engine"}
	r.fakeRunner.out = map[string]string{"skrog-engine": realOutput(t)}
	report, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 1 || !strings.Contains(strings.Join(report.Notes, " "), "access is denied") {
		t.Errorf("samples %d notes %v", len(report.Samples), report.Notes)
	}
	if s, ok := JSON(report)["wslc_sessions"].([]map[string]any); !ok || len(s) != 0 {
		t.Errorf("wslc_sessions %v", JSON(report)["wslc_sessions"])
	}
}

// --wsl: the wslc section is not shown, and wslc is not asked anything.
func TestOnlyTheWSLSection(t *testing.T) {
	out, list := sessionFixture(t)
	r := &sessionRunner{sessions: []string{"s"}, out: out, list: list}
	r.running = []string{"skrog-engine"}
	r.fakeRunner.out = map[string]string{"skrog-engine": realOutput(t)}
	report, err := Collect(context.Background(), r, Options{Sections: Sections{WSL: true}})
	if err != nil {
		t.Fatal(err)
	}
	if r.sampled != 0 || report.Sessions != nil {
		t.Errorf("wslc was asked with only --wsl: %d calls", r.sampled)
	}
	var b bytes.Buffer
	Render(&b, report)
	if !strings.Contains(b.String(), rule("utility VM")) || strings.Contains(b.String(), "wslc session") {
		t.Errorf("got:\n%s", b.String())
	}
	if _, ok := JSON(report)["wslc_sessions"]; ok {
		t.Error("JSON has a wslc section with only --wsl")
	}
}

// --wslc: the WSL section is not shown, and no distribution is measured.
func TestOnlyTheWSLCSection(t *testing.T) {
	out, list := sessionFixture(t)
	r := &sessionRunner{sessions: []string{"s"}, out: out, list: list}
	r.running = []string{"skrog-engine"}
	r.fakeRunner.out = map[string]string{"skrog-engine": realOutput(t)}
	report, err := Collect(context.Background(), r, Options{Sections: Sections{WSLC: true}})
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 || len(report.Samples) != 0 {
		t.Errorf("a distribution was measured with only --wslc: %d calls", r.calls)
	}
	var b bytes.Buffer
	Render(&b, report)
	got := b.String()
	if !strings.HasPrefix(got, rule("wslc session s (preview)")) || strings.Contains(got, "utility VM") {
		t.Errorf("got:\n%s", got)
	}
	o := JSON(report)
	if _, ok := o["vm"]; ok {
		t.Error("JSON has the utility VM with only --wslc")
	}
	if _, ok := o["distributions"]; ok {
		t.Error("JSON has distributions with only --wslc")
	}
	if s, ok := o["wslc_sessions"].([]map[string]any); !ok || len(s) != 1 {
		t.Errorf("wslc_sessions %v", o["wslc_sessions"])
	}
}

// --wslc with no session VM running: the section is shown, empty, and says
// why; nothing is started to fill it.
func TestTheWSLCSectionStaysEmpty(t *testing.T) {
	r := &sessionRunner{sessions: []string{}}
	report, err := Collect(context.Background(), r, Options{Sections: Sections{WSLC: true}})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	Render(&b, report)
	if !strings.HasPrefix(b.String(), rule("wslc")) || !strings.Contains(b.String(), "no wslc session VM is running") {
		t.Errorf("got:\n%s", b.String())
	}
	if s, ok := JSON(report)["wslc_sessions"].([]map[string]any); !ok || len(s) != 0 {
		t.Errorf("wslc_sessions %v", JSON(report)["wslc_sessions"])
	}
}

// By default: with wslc installed and no session running, the empty section
// says so; without wslc there is no section at all.
func TestTheDefaultShowsAnEmptyWSLCSectionOnlyWhereWSLCIs(t *testing.T) {
	for _, c := range []struct {
		name     string
		sessions []string
		want     bool
	}{
		{"wslc installed, nothing running", []string{}, true},
		{"no wslc", nil, false},
	} {
		r := &sessionRunner{sessions: c.sessions}
		r.running = []string{"skrog-engine"}
		r.fakeRunner.out = map[string]string{"skrog-engine": realOutput(t)}
		report, err := Collect(context.Background(), r, Options{})
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		Render(&b, report)
		if got := strings.Contains(b.String(), "no wslc session VM is running"); got != c.want {
			t.Errorf("%s: empty wslc section shown %v, want %v:\n%s", c.name, got, c.want, b.String())
		}
	}
}

// The session VM's vmmem is found by its boot time, like the utility VM's, and
// so it stops being an unnamed other VM.
func TestSessionClaimsItsVMMem(t *testing.T) {
	out, list := sessionFixture(t)
	s, _ := ParseSession("s", out, list)
	now := time.Now()
	boot, _ := s.Boot(now)
	h := Host{Others: []HostVM{{PID: 5, Created: boot.Add(400 * time.Millisecond)}, {PID: 6, Created: boot.Add(-time.Hour)}}}
	sessions := []Session{s}
	claimSessions(&h, sessions, now, time.Time{})
	if sessions[0].Host == nil || sessions[0].Host.PID != 5 {
		t.Fatalf("session host %+v", sessions[0].Host)
	}
	if len(h.Others) != 1 || h.Others[0].PID != 6 {
		t.Errorf("others %+v", h.Others)
	}
}

// A VM that booted during the measurement was started by it, and the report
// says so, because the user may not have wanted it running.
func TestSweepSaysWhenItStartedTheSessionVM(t *testing.T) {
	out, list := sessionFixture(t)
	s, _ := ParseSession("s", out, list)
	r := &sessionRunner{sessions: []string{"s"}, out: out, list: list}

	// The fixture's VM booted vm_at_csec ago. A sweep that began a minute
	// after that found it already running.
	up := time.Duration(s.VM.AtCsec) * 10 * time.Millisecond
	got, _, _ := sweepSessions(context.Background(), r, time.Second, time.Now().Add(-up+time.Minute))
	if got[0].Started {
		t.Error("a VM up since before the sweep was claimed as started by it")
	}
	// One that began ten seconds before the boot is what caused it.
	got, _, _ = sweepSessions(context.Background(), r, time.Second, time.Now().Add(-up-10*time.Second))
	if !got[0].Started {
		t.Error("a VM that booted after the sweep began was not claimed as started by it")
	}
	var b bytes.Buffer
	Render(&b, Report{Sessions: got})
	if !strings.Contains(b.String(), "asking wslc started it") {
		t.Errorf("got:\n%s", b.String())
	}
}

// Measured: a session VM booted by the sweep came up within a second of it
// starting, too close for the guest's clock to call. The vmmem's creation
// time is on the Windows clock, like the sweep's start, and settles it both
// ways.
func TestTheVMMemDecidesWhetherTheSweepStartedTheVM(t *testing.T) {
	out, list := sessionFixture(t)
	now := time.Now()
	s, _ := ParseSession("s", out, list)
	s.sampledAt = now
	boot, _ := s.Boot(now)

	for _, c := range []struct {
		name    string
		created time.Time
		started time.Time
		want    bool
	}{
		{"created 300 ms after the sweep began", boot, boot.Add(-300 * time.Millisecond), true},
		{"created before the sweep began", boot, boot.Add(300 * time.Millisecond), false},
	} {
		sessions := []Session{s}
		// Start from the opposite answer, so the vmmem is what decides.
		sessions[0].Started = !c.want
		h := Host{Others: []HostVM{{PID: 5, Created: c.created}}}
		claimSessions(&h, sessions, now, c.started)
		if sessions[0].Host == nil || sessions[0].Started != c.want {
			t.Errorf("%s: started %v, host %+v", c.name, sessions[0].Started, sessions[0].Host)
		}
	}
}

func TestSessionRatesCarryAcross(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	before := []Session{{Name: "s", Started: true, VM: VM{TotalBytes: 1, AtCsec: 100}, Containers: []Sample{{Distro: "c", Kind: KindWSLC, AtCsec: 100, WriteBytes: u(0)}}}}
	after := []Session{{Name: "s", VM: VM{TotalBytes: 1, AtCsec: 200, CPUUsec: 1_000_000}, Containers: []Sample{{Distro: "c", Kind: KindWSLC, AtCsec: 200, CPUUsec: 500_000, WriteBytes: u(1000)}}}}
	got := sessionRates(before, after, time.Second)
	c := got[0].Containers[0]
	if c.Rates.CPU == nil || *c.Rates.CPU < 49.9 || *c.Rates.CPU > 50.1 {
		t.Errorf("cpu %v", c.Rates.CPU)
	}
	if c.Rates.Write == nil || *c.Rates.Write != 1000 {
		t.Errorf("write %v", c.Rates.Write)
	}
	if got[0].VM.Rates.CPU == nil || *got[0].VM.Rates.CPU < 99.9 {
		t.Errorf("vm cpu %v", got[0].VM.Rates.CPU)
	}
	// Once started by the measurement, a --watch keeps saying so.
	if !got[0].Started {
		t.Error("started was lost between frames")
	}
}
