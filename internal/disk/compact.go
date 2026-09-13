package disk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CompactOptions controls a compaction.
type CompactOptions struct {
	// Trim runs fstrim first. On by default, and the reason compaction is
	// worth running at all: without it the disk still holds the stale data
	// and there is nothing to reclaim.
	Trim bool
	// Shutdown permits stopping every distribution, not just this one, when
	// the disk will not come free. Opt-in, because it stops the user's
	// containers too.
	Shutdown bool
	// Restart starts the distribution again afterwards if it was running.
	Restart bool
	// UnlockTimeout is how long to wait for the utility VM to let go of the
	// disk after the distribution stops.
	UnlockTimeout time.Duration
	// TrimTimeout bounds the fstrim.
	TrimTimeout time.Duration
	// RunningBefore is the set of distributions that were up before
	// anything was stopped. Compacting several disks with Shutdown set
	// stops them all on the first target, so without this snapshot every
	// later target would look as though it had never been running and
	// Restart would silently skip it.
	RunningBefore map[string]bool
}

// DefaultUnlockTimeout is how long to wait for the disk.
//
// With nothing else running, the utility VM releases a disk about 66 seconds
// after the last distribution stops, which tracks the 60-second idle timeout
// plus its own shutdown. Ninety seconds leaves room without making a genuine
// refusal feel like a hang.
const DefaultUnlockTimeout = 90 * time.Second

// MaxUnlockTimeout caps what a user can ask for, so a typo cannot wedge the
// command for an hour without saying why.
const MaxUnlockTimeout = time.Hour

const (
	// unlockPoll is how often the disk is asked whether it is free.
	unlockPoll = 500 * time.Millisecond
	// unlockTick is how often the countdown is redrawn.
	unlockTick = time.Second
)

// ErrBusy means the disk could not be freed within the timeout.
var ErrBusy = errors.New("disk: the virtual disk is still held open")

// CompactResult is the outcome for one target.
type CompactResult struct {
	// Label is the distribution name, or the path for a loose file.
	Label string
	// Before and After are the size on disk either side, when both could be
	// measured.
	Before, After *uint64
	// Trim is the trim result, when one was run.
	Trim *TrimResult
	// Err is the failure, if any. A failed target is still a result rather
	// than an aborted command, so `--all` can report every disk.
	Err error
}

// Reclaimed is what the compaction gave back, floored at zero.
func (r CompactResult) Reclaimed() *uint64 {
	if r.Before == nil || r.After == nil || *r.After >= *r.Before {
		if r.Before != nil && r.After != nil {
			zero := uint64(0)
			return &zero
		}
		return nil
	}
	v := *r.Before - *r.After
	return &v
}

// CompactTarget is what a compaction acts on: either a registered distribution
// or a loose .vhdx such as the one Docker Desktop keeps.
type CompactTarget struct {
	Reg  Registration
	Path string
}

// Label names the target in output.
func (t CompactTarget) Label() string {
	if t.Reg.Name != "" {
		return t.Reg.Name
	}
	return t.Path
}

// DiskPath is the file to compact.
func (t CompactTarget) DiskPath() string {
	if t.Reg.Name != "" {
		return t.Reg.VhdPath()
	}
	return t.Path
}

// PlanCompact describes what a compaction will do.
func PlanCompact(e Env, t CompactTarget, o CompactOptions) (Plan, error) {
	p := Plan{Subject: t.Label(), SubjectKey: "target"}

	if t.Reg.Name != "" && t.Reg.Version != 2 {
		return p, fmt.Errorf("%w: %s has no virtual disk to compact; convert it with wsl --set-version %s 2", ErrNotWSL2, t.Reg.Name, t.Reg.Name)
	}
	path := t.DiskPath()
	if path == "" {
		return p, fmt.Errorf("%w: %s", ErrNotVHDX, t.Label())
	}
	if !strings.HasSuffix(strings.ToLower(path), ".vhdx") {
		return p, fmt.Errorf("%w: %s", ErrNotVHDX, path)
	}
	if !e.FS.Exists(path) {
		return p, fmt.Errorf("disk: %s is not there. Run wslkit disk orphans to find disks nothing claims, or wslkit disk relink to repoint the distribution", path)
	}

	if t.Reg.Name != "" {
		if o.Trim {
			p.Add("run fstrim in %s", t.Reg.Name)
		}
		if o.Shutdown {
			p.Add("shut WSL down so the disk is released")
		} else {
			p.Add("stop %s and wait for its disk", t.Reg.Name)
		}
	}

	// Compaction rewrites the file in place. Everything that can refuse has
	// refused by now.
	p.AddIrreversible("compact %s", path)

	if t.Reg.Name != "" && o.Restart {
		p.Add("start %s again", t.Reg.Name)
	}

	p.Warn("compaction rewrites the disk file and cannot be undone",
		"nothing inside the distribution changes; only unused blocks go")
	if t.Reg.Name != "" && !o.Shutdown {
		p.Warn("the disk can only be released by stopping every distribution",
			"re-run with --shutdown if this refuses because something else is holding it")
	}
	return p, nil
}

// Compact reclaims the unused space in one disk.
// The result is a named return because the deferred restart below records its
// own failure into it; with an unnamed return that record would be written to a
// copy the caller never sees.
func Compact(ctx context.Context, e Env, t CompactTarget, o CompactOptions, pr Progress) (res CompactResult) {
	res.Label = t.Label()
	if pr == nil {
		pr = DiscardProgress{}
	}
	path := t.DiskPath()

	wasRunning := o.RunningBefore[t.Reg.Name]

	// A restart was promised if the distribution was up, so it happens on
	// the failure path too.
	defer func() {
		if t.Reg.Name == "" || !o.Restart || !wasRunning {
			return
		}
		pr.Step(fmt.Sprintf("start %s again", t.Reg.Name))
		if err := start(ctx, e, t.Reg.Name); err != nil && res.Err == nil {
			// Not fatal: the compaction is what was asked for and it
			// worked. But it must be said, because a distribution
			// that will not boot afterwards is the user's problem now.
			res.Err = fmt.Errorf("disk: %s was compacted but did not start again: %w", t.Reg.Name, err)
		}
	}()

	if t.Reg.Name != "" && o.Trim {
		pr.Step(fmt.Sprintf("run fstrim in %s", t.Reg.Name))
		tr, err := Trim(ctx, e, t.Reg, o.TrimTimeout)
		if err != nil {
			res.Err = err
			return res
		}
		res.Trim = &tr
		// The trim started the distribution, so it is running now
		// whatever it was doing before.
		wasRunning = true
	}

	if t.Reg.Name != "" {
		if err := release(ctx, e, t.Reg, o, path, pr); err != nil {
			res.Err = err
			return res
		}
	} else if locked, err := e.FS.Locked(path); err == nil && locked {
		res.Err = fmt.Errorf("%w: %s is open in another process. Close whatever is using it, with wsl --shutdown for WSL or by quitting Docker Desktop, and try again", ErrBusy, path)
		return res
	}

	// Measured here rather than at plan time: fstrim gets up to ten minutes,
	// and a build running in the guest can inflate the file in that window,
	// which made a compaction that worked look as though it had grown.
	if before, err := e.FS.SizeOnDisk(path); err == nil {
		res.Before = &before
	}

	pr.Step(fmt.Sprintf("compact %s", path))
	if err := e.Disks.Compact(ctx, path, pr.Fraction); err != nil {
		res.Err = err
		return res
	}

	if after, err := e.FS.SizeOnDisk(path); err == nil {
		res.After = &after
	}
	if res.Before != nil && res.After != nil && *res.After > *res.Before {
		res.Err = fmt.Errorf("disk: %s grew during compaction, from %s to %s", path, FormatSize(*res.Before), FormatSize(*res.After))
	}
	return res
}

// release stops the distribution and waits for the utility VM to let go of the
// disk.
func release(ctx context.Context, e Env, r Registration, o CompactOptions, path string, pr Progress) error {
	if o.Shutdown {
		pr.Step("shut WSL down so the disk is released")
		if err := e.Host.Shutdown(ctx); err != nil {
			return err
		}
	} else {
		pr.Step(fmt.Sprintf("stop %s and wait for its disk", r.Name))
		if err := e.Host.Terminate(ctx, r.Name); err != nil {
			return err
		}
	}

	// The wait runs either way. Skipping it after a shutdown was worse: the
	// compaction then failed at the open with a raw virtual disk error
	// rather than a named, explainable refusal.
	blockers, err := othersRunning(ctx, e, r.Name)
	if err != nil {
		blockers = nil
	}
	// While any distribution is running the utility VM never idles out, so
	// waiting cannot help and the refusal should be immediate.
	waitingCanHelp := len(blockers) == 0

	timeout := o.UnlockTimeout
	if timeout <= 0 {
		timeout = DefaultUnlockTimeout
	}
	if timeout > MaxUnlockTimeout {
		timeout = MaxUnlockTimeout
	}

	deadline := e.Clock.Now().Add(timeout)
	var lastTick time.Time
	for {
		// Asked before sleeping, so a zero timeout still gets an answer.
		locked, err := e.FS.Locked(path)
		if err == nil && !locked {
			return nil
		}
		now := e.Clock.Now()
		if !waitingCanHelp || !now.Before(deadline) {
			return busyError(r, o, path, blockers, timeout)
		}
		if now.Sub(lastTick) >= unlockTick {
			lastTick = now
			left := int(deadline.Sub(now).Seconds())
			pr.Status(fmt.Sprintf("waiting for the disk ... %ds of %ds", int(timeout.Seconds())-left, int(timeout.Seconds())))
		}
		e.Clock.Sleep(unlockPoll)
	}
}

// busyError explains why the disk is still held, which differs by situation and
// so takes a different remedy each time.
func busyError(r Registration, o CompactOptions, path string, blockers []string, timeout time.Duration) error {
	switch {
	case len(blockers) > 0:
		return fmt.Errorf("%w: %s is still open in %s. The WSL utility VM keeps every disk open while any distribution runs; re-run with --shutdown to stop them all, or close them yourself first",
			ErrBusy, path, strings.Join(blockers, ", "))
	case o.Shutdown:
		return fmt.Errorf("%w: WSL has been shut down and something else still has %s open. A backup agent, an antivirus scanner or Hyper-V Manager are the usual ones",
			ErrBusy, path)
	default:
		next := timeout * 2
		if next > MaxUnlockTimeout {
			next = MaxUnlockTimeout
		}
		return fmt.Errorf("%w: %s is still held by the WSL utility VM after %s. Nothing is running, so the VM is winding down; wait longer with --unlock-timeout %s, or re-run with --shutdown to stop it now",
			ErrBusy, path, timeout, next)
	}
}

// othersRunning lists the distributions that are up, excluding the target.
//
// The target is excluded because `wsl --list --running` can still name a
// distribution that has just been terminated.
func othersRunning(ctx context.Context, e Env, exclude string) ([]string, error) {
	names, err := e.Host.Running(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if !strings.EqualFold(n, exclude) {
			out = append(out, n)
		}
	}
	return out, nil
}

// start boots a distribution and checks it came up.
//
// /bin/sh -c : rather than /bin/true: POSIX guarantees the shell, and NixOS-WSL
// ships without /bin/true, where a missing binary boots the distribution and
// then fails the exec, which is indistinguishable from a distribution that will
// not start.
func start(ctx context.Context, e Env, name string) error {
	res, err := e.Host.RunAsRoot(ctx, name, []string{"/bin/sh", "-c", ":"}, 2*time.Minute)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("starting %s exited %d: %s", name, res.ExitCode, oneLine(res.Stderr))
	}
	return nil
}

// RenderCompact writes the human-readable result for one target.
func RenderCompact(w io.Writer, res CompactResult) {
	switch {
	case res.Err != nil:
		fmt.Fprintf(w, "%s: %v\n", res.Label, res.Err)
	case res.Reclaimed() != nil && res.Before != nil && res.After != nil:
		fmt.Fprintf(w, "%s: %s reclaimed (%s to %s)\n", res.Label,
			FormatSize(*res.Reclaimed()), FormatSize(*res.Before), FormatSize(*res.After))
	default:
		fmt.Fprintf(w, "%s: compacted. Its size could not be measured.\n", res.Label)
	}
}

// CompactJSON is the object printed per target for --json.
func CompactJSON(res CompactResult) map[string]any {
	o := map[string]any{
		"target":    res.Label,
		"compacted": res.Err == nil,
	}
	if res.Err != nil {
		o["error"] = res.Err.Error()
	}
	putU64(o, "size_before", res.Before)
	putU64(o, "size_after", res.After)
	putU64(o, "reclaimed", res.Reclaimed())
	if res.Trim != nil && res.Trim.Bytes != nil {
		o["trim_bytes_offered"] = *res.Trim.Bytes
	}
	return o
}
