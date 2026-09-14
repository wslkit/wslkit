package guard

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeWSL is a machine that can be told how to misbehave, and which records
// what was done to it.
type fakeWSL struct {
	// answers is consumed one probe at a time: each entry says whether
	// `wsl --status` and the distribution answer on that round, so a test
	// can make a rung "work".
	statusErr []error
	execErr   []error
	round     int

	distro     string
	running    []string
	calls      []string
	killErr    error
	restartErr error
	guestClock time.Time
	guestErr   error
	now        time.Time
	slept      time.Duration
	// healAfter makes every probe succeed once this many rungs have been
	// climbed, which is how a test says "the third step fixed it".
	healAfter int
	climbed   int
}

func (f *fakeWSL) Status(ctx context.Context, timeout time.Duration) error {
	if f.healAfter > 0 && f.climbed >= f.healAfter {
		return nil
	}
	return f.next(f.statusErr)
}

func (f *fakeWSL) Exec(ctx context.Context, distro string, timeout time.Duration) error {
	if f.healAfter > 0 && f.climbed >= f.healAfter {
		return nil
	}
	return f.next(f.execErr)
}

// next returns the answer for this round, repeating the last one for ever.
func (f *fakeWSL) next(list []error) error {
	if len(list) == 0 {
		return nil
	}
	i := f.round
	if i >= len(list) {
		i = len(list) - 1
	}
	return list[i]
}

func (f *fakeWSL) DefaultDistro(ctx context.Context) (string, error) { return f.distro, nil }
func (f *fakeWSL) Running(ctx context.Context) ([]string, error)     { return f.running, nil }

func (f *fakeWSL) Shutdown(ctx context.Context, force bool, timeout time.Duration) error {
	f.calls = append(f.calls, map[bool]string{false: "shutdown", true: "shutdown --force"}[force])
	f.climbed++
	f.round++
	return nil
}

func (f *fakeWSL) KillService(ctx context.Context, timeout time.Duration) error {
	f.calls = append(f.calls, "kill")
	f.climbed++
	f.round++
	return f.killErr
}

func (f *fakeWSL) RestartService(ctx context.Context, timeout time.Duration) error {
	f.calls = append(f.calls, "restart")
	f.climbed++
	f.round++
	return f.restartErr
}

func (f *fakeWSL) GuestClock(ctx context.Context, distro string, timeout time.Duration) (time.Time, error) {
	return f.guestClock, f.guestErr
}

func (f *fakeWSL) StepClock(ctx context.Context, distro string, timeout time.Duration) error {
	f.calls = append(f.calls, "step-clock")
	return nil
}

func (f *fakeWSL) Now() time.Time        { return f.now }
func (f *fakeWSL) Sleep(d time.Duration) { f.slept += d }

func newFake() *fakeWSL {
	now := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	// The guest clock agrees with the host unless a test says otherwise: a
	// zero-valued clock is decades out and would make every test a clock test.
	return &fakeWSL{distro: "Ubuntu", now: now, guestClock: now}
}

// The ordinary morning: the lid opens, everything answers, and an unattended
// tool must do nothing at all.
func TestHealthyMachineIsLeftAlone(t *testing.T) {
	f := newFake()
	rep := Run(context.Background(), f, Options{}, []string{"Ubuntu"}, true, nil)
	if !rep.Recovered || len(rep.Attempted) != 0 {
		t.Fatalf("attempted %+v", rep.Attempted)
	}
	if len(f.calls) != 0 {
		t.Errorf("a healthy machine was interfered with: %v", f.calls)
	}
}

// The case that would get this uninstalled: a distribution that was not running
// before the machine slept is stopped, not broken, and starting one from cold
// looks exactly like a hang.
func TestColdStartIsNotAFailure(t *testing.T) {
	f := newFake()
	f.execErr = []error{context.DeadlineExceeded}
	rep := Run(context.Background(), f, Options{}, nil, true, nil)

	if len(rep.Attempted) != 0 {
		t.Fatalf("nothing should have been done: %+v", rep.Attempted)
	}
	if len(f.calls) != 0 {
		t.Errorf("the VM was interfered with: %v", f.calls)
	}
	if !strings.Contains(rep.Decision.Reason, "not running before") {
		t.Errorf("reason = %q", rep.Decision.Reason)
	}
}

// And with no record either way, it still does nothing: acting on a guess is
// how a scheduled task ends up shutting WSL down every morning.
func TestNoRecordMeansNoAction(t *testing.T) {
	f := newFake()
	f.execErr = []error{context.DeadlineExceeded}
	rep := Run(context.Background(), f, Options{}, nil, false, nil)
	if len(f.calls) != 0 {
		t.Errorf("acted without evidence: %v", f.calls)
	}
	if !strings.Contains(rep.Decision.Reason, "cold start") {
		t.Errorf("reason = %q", rep.Decision.Reason)
	}
}

// The real thing: it was running, it hangs now, and the first rung fixes it.
func TestHungDistroRecoveredByTheFirstRung(t *testing.T) {
	f := newFake()
	f.execErr = []error{context.DeadlineExceeded}
	f.healAfter = 1

	var log strings.Builder
	rep := Run(context.Background(), f, Options{}, []string{"Ubuntu"}, true, &log)

	if !rep.Recovered {
		t.Fatalf("not recovered: %+v", rep)
	}
	if len(rep.Attempted) != 1 || rep.Attempted[0].Rung != RungShutdown {
		t.Fatalf("attempted %+v, want one polite shutdown", rep.Attempted)
	}
	if got := strings.Join(f.calls, ","); got != "shutdown" {
		t.Errorf("calls = %q: the ladder should stop at the rung that worked", got)
	}
	if !strings.Contains(log.String(), "WSL answers again after wsl --shutdown") {
		t.Errorf("log = %q", log.String())
	}
}

// A wedged service climbs further, and stops at the rung that works.
func TestWedgedServiceClimbsUntilSomethingWorks(t *testing.T) {
	f := newFake()
	f.statusErr = []error{context.DeadlineExceeded}
	f.healAfter = 3 // the third rung, taskkill, is the one that works

	rep := Run(context.Background(), f, Options{Elevated: true}, []string{"Ubuntu"}, true, nil)
	if !rep.Recovered {
		t.Fatalf("not recovered: %+v", rep.Attempted)
	}
	want := "shutdown,shutdown --force,kill"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// Without administrator rights the last two rungs are not attempted at all. A
// log full of access-denied lines teaches the reader to ignore the log.
func TestUnelevatedStopsBeforeTheAdminRungs(t *testing.T) {
	f := newFake()
	f.statusErr = []error{context.DeadlineExceeded}

	rep := Run(context.Background(), f, Options{Elevated: false}, []string{"Ubuntu"}, true, nil)
	for _, a := range rep.Attempted {
		if a.Rung.NeedsElevation() {
			t.Errorf("attempted %s without elevation", a.Rung)
		}
	}
	if got := strings.Join(f.calls, ","); got != "shutdown,shutdown --force" {
		t.Errorf("calls = %q", got)
	}
}

// The two services that are never touched, whatever happens: stopping vmcompute
// has been reported to bluescreen the machine.
func TestTheLadderNeverTouchesVmcomputeOrHvHost(t *testing.T) {
	f := newFake()
	f.statusErr = []error{context.DeadlineExceeded}
	Run(context.Background(), f, Options{Elevated: true}, []string{"Ubuntu"}, true, nil)
	for _, c := range f.calls {
		if strings.Contains(strings.ToLower(c), "vmcompute") || strings.Contains(strings.ToLower(c), "hvhost") {
			t.Fatalf("the ladder touched %q", c)
		}
	}
	// And the ladder is exactly the four documented rungs.
	if got := strings.Join(f.calls, ","); got != "shutdown,shutdown --force,kill,restart" {
		t.Errorf("calls = %q", got)
	}
}

// A clock hours out breaks TLS, and chrony slews rather than steps after a long
// sleep, so nothing fixes it on its own.
func TestClockSkewIsStepped(t *testing.T) {
	f := newFake()
	f.guestClock = f.now.Add(-90 * time.Second)
	rep := Run(context.Background(), f, Options{}, []string{"Ubuntu"}, true, nil)
	if !rep.ClockStepped {
		t.Fatalf("a 90-second offset should have been stepped: %+v", rep.Health)
	}
	if !strings.Contains(strings.Join(f.calls, ","), "step-clock") {
		t.Errorf("calls = %v", f.calls)
	}
}

// A second or two is chrony's job, not this one's.
func TestSmallSkewIsLeftToChrony(t *testing.T) {
	f := newFake()
	f.guestClock = f.now.Add(2 * time.Second)
	rep := Run(context.Background(), f, Options{}, []string{"Ubuntu"}, true, nil)
	if rep.ClockStepped {
		t.Error("two seconds is not worth stepping the clock for")
	}
}

// A dry run decides everything and does nothing.
func TestDryRunTouchesNothing(t *testing.T) {
	f := newFake()
	f.statusErr = []error{context.DeadlineExceeded}
	var log strings.Builder
	rep := Run(context.Background(), f, Options{Elevated: true, DryRun: true}, []string{"Ubuntu"}, true, &log)
	if len(f.calls) != 0 {
		t.Errorf("a dry run did something: %v", f.calls)
	}
	if len(rep.Decision.Ladder) == 0 || !strings.Contains(log.String(), "would try") {
		t.Errorf("a dry run should still say what it would do: %q", log.String())
	}
}

// MaxRung is for somebody who wants the polite step and nothing else.
func TestMaxRungCapsTheLadder(t *testing.T) {
	f := newFake()
	f.statusErr = []error{context.DeadlineExceeded}
	Run(context.Background(), f, Options{Elevated: true, MaxRung: RungShutdown}, []string{"Ubuntu"}, true, nil)
	if got := strings.Join(f.calls, ","); got != "shutdown" {
		t.Errorf("calls = %q", got)
	}
}

// A failure that is not a hang is not a hang: "WSL is not installed" must not
// start the ladder.
func TestPlainErrorIsNotTreatedAsAHang(t *testing.T) {
	f := newFake()
	f.statusErr = []error{errors.New("WSL is not installed")}
	rep := Run(context.Background(), f, Options{Elevated: true}, []string{"Ubuntu"}, true, nil)
	if len(f.calls) != 0 {
		t.Errorf("a plain error started the ladder: %v", f.calls)
	}
	if rep.Health.ServiceHung {
		t.Error("an error is not a hang")
	}
}

func TestParseRung(t *testing.T) {
	for in, want := range map[string]Rung{
		"":         RungNone,
		"all":      RungNone,
		"shutdown": RungShutdown,
		"force":    RungForceShutdown,
		"kill":     RungKillService,
		"restart":  RungRestartService,
	} {
		got, ok := ParseRung(in)
		if !ok || got != want {
			t.Errorf("ParseRung(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := ParseRung("vmcompute"); ok {
		t.Error("there is no such rung, and there never will be")
	}
}

// The record of what was running is the only evidence separating a hang from a
// cold start, so it has to survive a round trip and it has to expire.
func TestStateRoundTripAndExpiry(t *testing.T) {
	path := t.TempDir() + "/guard-state.json"
	now := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)

	if _, ok := LoadState(path); ok {
		t.Fatal("there is no record yet, and inventing one is how a guess becomes a measurement")
	}
	if err := SaveState(path, []string{"Ubuntu"}, now); err != nil {
		t.Fatal(err)
	}
	s, ok := LoadState(path)
	if !ok || len(s.Running) != 1 || s.Running[0] != "Ubuntu" {
		t.Fatalf("state = %+v, %v", s, ok)
	}
	if s.Stale(now.Add(time.Hour), MaxStateAge) {
		t.Error("an hour later is still this morning")
	}
	if !s.Stale(now.Add(48*time.Hour), MaxStateAge) {
		t.Error("a record from two days ago is not evidence about now")
	}
	// A clock that went backwards leaves a record from the future, which is
	// not evidence either.
	if !s.Stale(now.Add(-time.Hour), MaxStateAge) {
		t.Error("a record stamped in the future is not to be trusted")
	}
}

// Rubbish in the file reads as "no record" rather than as an error: the
// decision that follows is to do nothing, which is the safe one.
func TestUnreadableStateIsNoRecord(t *testing.T) {
	path := t.TempDir() + "/guard-state.json"
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadState(path); ok {
		t.Error("a corrupt file must not be read as evidence")
	}
}
