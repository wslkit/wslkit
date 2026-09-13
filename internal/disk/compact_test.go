package disk

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// compactEnv builds an environment where the disk exists and comes free after
// the given number of checks.
func compactEnv(unlockAfter int, disks *fakeDisks, host *fakeHost) (Env, *fakeFS, *fakeClock) {
	fs := &fakeFS{
		files:       map[string]fakeFile{ubuntuVhd: {size: 10 << 30, onDisk: 10 << 30}},
		unlockAfter: map[string]int{ubuntuVhd: unlockAfter},
	}
	if disks == nil {
		disks = &fakeDisks{}
	}
	if host == nil {
		host = &fakeHost{}
	}
	clock := &fakeClock{now: time.Unix(0, 0)}
	return Env{FS: fs, Disks: disks, Host: host, Clock: clock}, fs, clock
}

// shrinkingDisks reports a smaller size on disk after compacting, the way a
// successful compaction does.
type shrinkingDisks struct {
	fs    *fakeFS
	after uint64
	calls int
}

func (d *shrinkingDisks) Facts(path string) (DiskFacts, error) { return DiskFacts{}, nil }

func (d *shrinkingDisks) Compact(ctx context.Context, path string, progress func(uint64, uint64) bool) error {
	d.calls++
	f := d.fs.files[path]
	f.onDisk = d.after
	d.fs.files[path] = f
	if progress != nil {
		progress(1, 1)
	}
	return nil
}

func TestCompactReportsWhatItReclaimed(t *testing.T) {
	e, fs, _ := compactEnv(0, nil, nil)
	sd := &shrinkingDisks{fs: fs, after: 6 << 30}
	e.Disks = sd

	res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{}, nil)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if sd.calls != 1 {
		t.Fatalf("expected one compaction, got %d", sd.calls)
	}
	if got := res.Reclaimed(); got == nil || *got != 4<<30 {
		t.Errorf("reclaimed: %v", got)
	}
}

// Compaction can legitimately reclaim nothing: it works in whole VHDX blocks,
// so free space scattered in small holes leaves every block partly occupied.
// That is a zero, not a failure.
func TestCompactReclaimingNothingIsNotAFailure(t *testing.T) {
	e, fs, _ := compactEnv(0, nil, nil)
	e.Disks = &shrinkingDisks{fs: fs, after: 10 << 30}
	res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{}, nil)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if got := res.Reclaimed(); got == nil || *got != 0 {
		t.Errorf("reclaimed should be zero, got %v", got)
	}
}

func TestReclaimedFloorsAtZero(t *testing.T) {
	if got := (CompactResult{Before: u64(10), After: u64(20)}).Reclaimed(); got == nil || *got != 0 {
		t.Errorf("a disk that grew must not report negative reclaim: %v", got)
	}
	if got := (CompactResult{Before: u64(10)}).Reclaimed(); got != nil {
		t.Errorf("without both measurements it is unknowable: %v", got)
	}
}

// The disk is not free the instant the distribution stops: the utility VM holds
// it for about a minute. Waiting is the difference between working and telling
// the user their disk is busy.
func TestCompactWaitsForTheUtilityVMToReleaseTheDisk(t *testing.T) {
	e, fs, clock := compactEnv(4, nil, nil)
	e.Disks = &shrinkingDisks{fs: fs, after: 6 << 30}

	res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{}, nil)
	if res.Err != nil {
		t.Fatalf("should have waited and succeeded, got %v", res.Err)
	}
	if len(clock.slept) == 0 {
		t.Error("it should have waited at all")
	}
	// The wait must be bounded by the poll interval, not by a busy loop.
	for _, d := range clock.slept {
		if d != unlockPoll {
			t.Errorf("slept %v, want %v", d, unlockPoll)
		}
	}
}

// Three different reasons the disk is still held, three different things to do
// about it. Collapsing them into one message sends users down the wrong path.
func TestCompactExplainsWhyTheDiskIsStillHeld(t *testing.T) {
	t.Run("another distribution is running", func(t *testing.T) {
		// While anything runs, the utility VM never idles out, so waiting
		// cannot help and the refusal should be immediate.
		e, _, clock := compactEnv(1000, nil, &fakeHost{running: []string{"Ubuntu", "docker-desktop"}})
		res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{}, nil)
		if !errors.Is(res.Err, ErrBusy) {
			t.Fatalf("want ErrBusy, got %v", res.Err)
		}
		if !strings.Contains(res.Err.Error(), "docker-desktop") {
			t.Errorf("it should name what is holding it: %v", res.Err)
		}
		if !strings.Contains(res.Err.Error(), "--shutdown") {
			t.Errorf("it should offer the way out: %v", res.Err)
		}
		if len(clock.slept) != 0 {
			t.Errorf("waiting cannot help here, so it should not wait: %v", clock.slept)
		}
	})

	t.Run("nothing is running", func(t *testing.T) {
		e, _, _ := compactEnv(1000, nil, nil)
		res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{UnlockTimeout: 5 * time.Second}, nil)
		if !errors.Is(res.Err, ErrBusy) {
			t.Fatalf("want ErrBusy, got %v", res.Err)
		}
		if !strings.Contains(res.Err.Error(), "winding down") {
			t.Errorf("it should explain the VM is still going: %v", res.Err)
		}
		if !strings.Contains(res.Err.Error(), "--unlock-timeout") {
			t.Errorf("it should suggest waiting longer: %v", res.Err)
		}
	})

	t.Run("after a shutdown", func(t *testing.T) {
		e, _, _ := compactEnv(1000, nil, nil)
		res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{Shutdown: true, UnlockTimeout: time.Second}, nil)
		if !errors.Is(res.Err, ErrBusy) {
			t.Fatalf("want ErrBusy, got %v", res.Err)
		}
		// WSL is already down, so the remaining suspects are not WSL.
		if !strings.Contains(res.Err.Error(), "antivirus") {
			t.Errorf("it should name what else holds disk files: %v", res.Err)
		}
	})
}

// The wait runs even after --shutdown. Skipping it made the compaction fail at
// the open with a raw error instead of a named, explainable refusal.
func TestCompactWaitsEvenAfterAShutdown(t *testing.T) {
	e, fs, clock := compactEnv(3, nil, nil)
	e.Disks = &shrinkingDisks{fs: fs, after: 1 << 30}
	res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{Shutdown: true}, nil)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(clock.slept) == 0 {
		t.Error("it should still have waited")
	}
}

func TestCompactShutdownStopsEverythingAndTerminateStopsOne(t *testing.T) {
	e, fs, _ := compactEnv(0, nil, nil)
	e.Disks = &shrinkingDisks{fs: fs, after: 1 << 30}
	host := e.Host.(*fakeHost)
	Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{Shutdown: true}, nil)
	if len(host.calls) == 0 || host.calls[0] != "shutdown" {
		t.Errorf("expected a shutdown, got %v", host.calls)
	}

	e2, fs2, _ := compactEnv(0, nil, nil)
	e2.Disks = &shrinkingDisks{fs: fs2, after: 1 << 30}
	host2 := e2.Host.(*fakeHost)
	Compact(context.Background(), e2, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{}, nil)
	if len(host2.calls) == 0 || host2.calls[0] != "terminate Ubuntu" {
		t.Errorf("expected a terminate, got %v", host2.calls)
	}
}

// A restart was promised for a distribution that was up. It has to happen even
// when the compaction failed, or the user is left with their distribution
// stopped and no explanation.
func TestCompactRestartsAfterAFailureToo(t *testing.T) {
	e, _, _ := compactEnv(1000, nil, nil)
	host := e.Host.(*fakeHost)
	o := CompactOptions{Restart: true, UnlockTimeout: time.Second, RunningBefore: map[string]bool{"Ubuntu": true}}
	res := Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, o, nil)
	if res.Err == nil {
		t.Fatal("the compaction should have failed")
	}
	if !strings.Contains(strings.Join(host.calls, "; "), "/bin/sh") {
		t.Errorf("it should have started the distribution again: %v", host.calls)
	}
}

// Booting is checked with /bin/sh -c : rather than /bin/true, which NixOS-WSL
// does not ship: a missing binary boots the distribution and then fails the
// exec, which looks exactly like a distribution that will not start.
func TestCompactChecksTheRestartWithAShellNotBinTrue(t *testing.T) {
	e, fs, _ := compactEnv(0, nil, nil)
	e.Disks = &shrinkingDisks{fs: fs, after: 1 << 30}
	host := e.Host.(*fakeHost)
	o := CompactOptions{Restart: true, RunningBefore: map[string]bool{"Ubuntu": true}}
	Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, o, nil)
	joined := strings.Join(host.calls, "; ")
	if strings.Contains(joined, "/bin/true") {
		t.Errorf("must not use /bin/true: %v", host.calls)
	}
	if !strings.Contains(joined, "/bin/sh -c :") {
		t.Errorf("expected the shell probe: %v", host.calls)
	}
}

// Compacting several disks with --shutdown stops them all on the first target.
// Without a snapshot taken beforehand, every later target looks as though it
// was never running and --restart silently skips it.
func TestCompactUsesTheRunningSnapshotNotTheLiveState(t *testing.T) {
	e, fs, _ := compactEnv(0, nil, nil)
	e.Disks = &shrinkingDisks{fs: fs, after: 1 << 30}
	host := e.Host.(*fakeHost) // reports nothing running, as after a shutdown
	o := CompactOptions{Restart: true, RunningBefore: map[string]bool{"Ubuntu": true}}
	Compact(context.Background(), e, CompactTarget{Reg: reg("Ubuntu")}, o, nil)
	if !strings.Contains(strings.Join(host.calls, "; "), "/bin/sh") {
		t.Errorf("the snapshot should have driven the restart: %v", host.calls)
	}

	// And a distribution that was genuinely stopped is left stopped.
	e2, fs2, _ := compactEnv(0, nil, nil)
	e2.Disks = &shrinkingDisks{fs: fs2, after: 1 << 30}
	host2 := e2.Host.(*fakeHost)
	Compact(context.Background(), e2, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{Restart: true}, nil)
	if strings.Contains(strings.Join(host2.calls, "; "), "/bin/sh") {
		t.Errorf("a stopped distribution should be left stopped: %v", host2.calls)
	}
}

func TestPlanCompactRefusals(t *testing.T) {
	e, _, _ := compactEnv(0, nil, nil)
	if _, err := PlanCompact(e, CompactTarget{Reg: reg("Legacy", func(r *Registration) { r.Version = 1 })}, CompactOptions{}); !errors.Is(err, ErrNotWSL2) {
		t.Errorf("WSL 1: want ErrNotWSL2, got %v", err)
	}
	if _, err := PlanCompact(e, CompactTarget{Path: `C:\disks\data.vhd`}, CompactOptions{}); !errors.Is(err, ErrNotVHDX) {
		t.Errorf("a .vhd is not a .vhdx: got %v", err)
	}
	_, err := PlanCompact(e, CompactTarget{Reg: reg("Gone", func(r *Registration) { r.BasePath = `C:\nowhere` })}, CompactOptions{})
	if err == nil || !strings.Contains(err.Error(), "relink") {
		t.Errorf("a missing disk should point at relink: %v", err)
	}
}

// The plan is what --dry-run prints, so it has to describe the run accurately.
func TestPlanCompactDescribesTheRun(t *testing.T) {
	e, _, _ := compactEnv(0, nil, nil)
	p, err := PlanCompact(e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{Trim: true, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	RenderDryRun(&b, p, "")
	out := b.String()
	for _, want := range []string{"run fstrim in Ubuntu", "stop Ubuntu and wait for its disk", "compact ", "start Ubuntu again", "cannot be undone"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, out)
		}
	}
	if err := p.Valid(); err != nil {
		t.Errorf("plan should be valid: %v", err)
	}
}

func TestPlanCompactWithShutdownDropsTheRetryAdvice(t *testing.T) {
	e, _, _ := compactEnv(0, nil, nil)
	with, _ := PlanCompact(e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{Shutdown: true})
	for _, w := range with.Warnings {
		if strings.Contains(w.Remedy, "re-run with --shutdown") {
			t.Error("do not suggest a flag that is already set")
		}
	}
	without, _ := PlanCompact(e, CompactTarget{Reg: reg("Ubuntu")}, CompactOptions{})
	found := false
	for _, w := range without.Warnings {
		if strings.Contains(w.Remedy, "re-run with --shutdown") {
			found = true
		}
	}
	if !found {
		t.Error("without the flag, it should be offered")
	}
}

// A plan that schedules an undoable change after the point of no return is a
// programming mistake: past that point there is no rollback to offer.
func TestPlanRejectsAReversibleStepAfterAnIrreversibleOne(t *testing.T) {
	var p Plan
	p.Add("stop the distribution")
	p.AddIrreversible("compact the disk")
	if err := p.Valid(); err != nil {
		t.Fatalf("this order is fine: %v", err)
	}
	// Starting the distribution again afterwards changes nothing that would
	// need taking back, so it is allowed past that point.
	p.Add("start the distribution again")
	if err := p.Valid(); err != nil {
		t.Fatalf("a step with no rollback is fine after the point of no return: %v", err)
	}
	// A step that would register a rollback is not.
	p.AddUndoable("move the file back")
	err := p.Valid()
	if err == nil {
		t.Fatal("expected the invalid order to be caught")
	}
	if !strings.Contains(err.Error(), "bug in wslkit") {
		t.Errorf("it should be reported as a bug, not as user error: %v", err)
	}
}

func TestCompactJSON(t *testing.T) {
	o := CompactJSON(CompactResult{Label: "Ubuntu", Before: u64(10 << 30), After: u64(6 << 30)})
	if o["compacted"] != true || o["target"] != "Ubuntu" {
		t.Errorf("unexpected: %v", o)
	}
	if o["reclaimed"] != uint64(4<<30) {
		t.Errorf("reclaimed = %v", o["reclaimed"])
	}
	// A failed target is still an object, so --all can report every disk.
	f := CompactJSON(CompactResult{Label: "Ubuntu", Err: errors.New("busy")})
	if f["compacted"] != false || f["error"] != "busy" {
		t.Errorf("a failure should be reported in the same shape: %v", f)
	}
	if _, ok := f["reclaimed"]; ok {
		t.Error("nothing was reclaimed and nothing should be claimed")
	}
}

func TestRenderCompact(t *testing.T) {
	var b bytes.Buffer
	RenderCompact(&b, CompactResult{Label: "Ubuntu", Before: u64(15 << 30), After: u64(11 << 30)})
	if got := b.String(); got != "Ubuntu: 4.0 GiB reclaimed (15.0 GiB to 11.0 GiB)\n" {
		t.Errorf("got %q", got)
	}
	var b2 bytes.Buffer
	RenderCompact(&b2, CompactResult{Label: "Ubuntu"})
	if !strings.Contains(b2.String(), "could not be measured") {
		t.Errorf("got %q", b2.String())
	}
}
