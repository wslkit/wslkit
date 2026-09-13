package disk

import (
	"context"
	"errors"
	"fmt"
)

// Undo is a rollback entry: something that puts one change back.
type Undo struct {
	Description string
	Run         func() error
}

// UndoStack collects rollbacks and unwinds them in reverse.
type UndoStack struct {
	entries []Undo
}

// Push records a rollback.
func (s *UndoStack) Push(description string, run func() error) {
	s.entries = append(s.entries, Undo{Description: description, Run: run})
}

// Len is how many rollbacks are pending.
func (s *UndoStack) Len() int { return len(s.entries) }

// Unwind runs the rollbacks newest first, and keeps going after a failure: a
// rollback that cannot complete is worse than one that completes partly, and
// stopping would leave more of the change in place, not less.
func (s *UndoStack) Unwind() error {
	var failures []error
	for i := len(s.entries) - 1; i >= 0; i-- {
		if err := s.entries[i].Run(); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", s.entries[i].Description, err))
		}
	}
	s.entries = nil
	return errors.Join(failures...)
}

// ErrSmokeTest means the distribution would not start from its new disk.
var ErrSmokeTest = errors.New("disk: the distribution did not start")

// RelinkResult is the outcome.
type RelinkResult struct {
	Distro   string
	VhdPath  string
	BasePath string
}

// PlanRelink describes repointing a distribution at a disk.
func PlanRelink(e Env, r Registration, target string, running bool, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		return p, fmt.Errorf("%w: %s has no virtual disk to point anywhere", ErrNotWSL2, r.Name)
	}
	if !e.FS.Exists(target) {
		return p, fmt.Errorf("%w: %s is not there, so %s cannot be pointed at it", ErrRefused, target, r.Name)
	}
	if SamePath(r.VhdPath(), target) {
		return p, fmt.Errorf("%w: %s already points at %s", ErrRefused, r.Name, target)
	}
	// A failed query is itself a refusal. If the distribution is in fact
	// running, the smoke test below executes in the already-booted guest,
	// which booted from the old disk, and passes without testing anything.
	if !runningKnown {
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and repointing a running distribution cannot be verified", ErrRefused, r.Name)
	}
	if running {
		return p, fmt.Errorf("%w: stop %s first with wsl --terminate %s", ErrRunning, r.Name, r.Name)
	}

	p.AddUndoable("point %s at %s", r.Name, target)
	p.Add("start %s to check the new path works", r.Name)
	p.Warn("this rewrites the WSL registry entry for the distribution",
		"the previous values are put back if the distribution does not start")
	return p, nil
}

// Relink points a distribution at a disk that has moved.
//
// It writes registry values and nothing else: no file is touched. If the
// distribution will not boot from the new path, the registry is put back.
func Relink(ctx context.Context, e Env, r Registration, target string, pr Progress) (RelinkResult, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	res := RelinkResult{Distro: r.Name, VhdPath: target}
	var undo UndoStack

	intendedBase := WithSamePrefixAs(r.BasePath, DirOf(target))
	res.BasePath = intendedBase

	pr.Step(fmt.Sprintf("point %s at %s", r.Name, target))
	if err := repoint(e, r, target, intendedBase, &undo); err != nil {
		if uerr := undo.Unwind(); uerr != nil {
			return res, errors.Join(err, fmt.Errorf("and the rollback did not complete: %w", uerr))
		}
		return res, err
	}

	pr.Step(fmt.Sprintf("start %s to check the new path works", r.Name))
	if err := start(ctx, e, r.Name); err != nil {
		wrapped := fmt.Errorf("%w from %s: %v. The registry entry has been put back and the disk is untouched", ErrSmokeTest, target, err)
		if uerr := undo.Unwind(); uerr != nil {
			return res, errors.Join(wrapped, fmt.Errorf("and the rollback did not complete: %w", uerr))
		}
		return res, wrapped
	}

	// Read back what was written. An unwritten value compares unequal, which
	// is correct: the intended path is never empty.
	got, _, err := e.Registry.ReadString(r.GUID, "BasePath")
	if err == nil && got != intendedBase {
		return res, fmt.Errorf("disk: BasePath reads back as %q, not %q", got, intendedBase)
	}
	return res, nil
}

// repoint writes the registration, recording how to put it back.
func repoint(e Env, r Registration, target, intendedBase string, undo *UndoStack) error {
	// Captured before anything is written.
	prevBase, _, err := e.Registry.ReadString(r.GUID, "BasePath")
	if err != nil {
		return err
	}
	prevName, hadName, err := e.Registry.ReadString(r.GUID, "VhdFileName")
	if err != nil {
		return err
	}

	if err := e.Registry.WriteString(r.GUID, "BasePath", intendedBase); err != nil {
		return err
	}
	undo.Push("restore BasePath", func() error {
		return e.Registry.WriteString(r.GUID, "BasePath", prevBase)
	})

	// Only rewrite the file name if the registration already carried one.
	// Adding the value to a legacy entry that never had it would change its
	// layout rather than repair its path.
	if hadName {
		if err := e.Registry.WriteString(r.GUID, "VhdFileName", BaseOf(target)); err != nil {
			return err
		}
		undo.Push("restore VhdFileName", func() error {
			return e.Registry.WriteString(r.GUID, "VhdFileName", prevName)
		})
	} else if !SamePath(BaseOf(target), DefaultVhdName) {
		// The registration relies on the default name, and the new disk
		// is not called that, so pointing at it would silently open the
		// wrong file.
		return fmt.Errorf("disk: %s has no VhdFileName and so relies on the default %s, but the disk is called %s; rename it or use wslkit disk move",
			r.Name, DefaultVhdName, BaseOf(target))
	}
	return nil
}

// RelinkJSON is the object printed for --json.
func RelinkJSON(res RelinkResult) map[string]any {
	return map[string]any{
		"distribution": res.Distro,
		"vhdx_path":    res.VhdPath,
		"base_path":    res.BasePath,
		"relinked":     true,
	}
}
