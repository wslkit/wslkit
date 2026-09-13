package disk

import (
	"context"
	"errors"
	"fmt"
)

// FreeSpaceMargin is the headroom required beyond the size of the disk itself.
//
// Filling a volume to the last byte is how a move that technically succeeded
// leaves a machine that cannot boot a distribution afterwards.
const FreeSpaceMargin = 64 << 20

// MoveResult is the outcome.
type MoveResult struct {
	Distro     string
	VhdPath    string
	BasePath   string
	Renamed    bool
	SameVolume bool
	KeptSource bool
	SizeOnDisk *uint64
}

// MoveOptions controls a move.
type MoveOptions struct {
	// KeepSource leaves the original file where it was, which turns a
	// same-volume move into a copy.
	KeepSource bool
}

// MoveTarget is where a disk is going.
type MoveTarget struct {
	// Dir is the directory the disk moves into. The file keeps its name.
	Dir string
	// Path is the full destination path.
	Path string
}

// NewMoveTarget works out where the disk lands. The file keeps its name, so the
// destination is a directory, not a file.
func NewMoveTarget(source, dir string) MoveTarget {
	return MoveTarget{Dir: dir, Path: joinWindows(dir, BaseOf(source))}
}

// PlanMove describes moving a distribution disk.
func PlanMove(e Env, r Registration, t MoveTarget, o MoveOptions, running bool, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		return p, fmt.Errorf("%w: %s has no virtual disk to move", ErrNotWSL2, r.Name)
	}
	source := r.VhdPath()
	if !e.FS.Exists(source) {
		return p, fmt.Errorf("%w: %s is not there. Run wslkit disk orphans to find it, then wslkit disk relink", ErrRefused, source)
	}
	if SamePath(source, t.Path) {
		return p, fmt.Errorf("%w: %s is already at %s", ErrRefused, r.Name, t.Path)
	}
	// The likeliest thing sitting at the destination is the user's own
	// previous attempt, so it is never overwritten.
	if e.FS.Exists(t.Path) {
		return p, fmt.Errorf("%w: %s already exists; move it aside or choose another directory", ErrRefused, t.Path)
	}
	if !runningKnown {
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and a move cannot be verified while it might be", ErrRefused, r.Name)
	}
	if running {
		return p, fmt.Errorf("%w: stop %s first with wsl --terminate %s", ErrRunning, r.Name, r.Name)
	}

	same, err := e.FS.SameVolume(source, t.Dir)
	if err != nil {
		// Not knowing means the copy path, which is correct everywhere.
		same = false
	}
	rename := same && !o.KeepSource

	if !rename {
		if err := checkDestination(e, source, t.Dir); err != nil {
			return p, err
		}
	}

	if rename {
		p.AddUndoable("move %s to %s", source, t.Path)
	} else {
		p.AddUndoable("copy %s to %s", source, t.Path)
	}
	p.AddUndoable("point %s at %s", r.Name, t.Path)
	p.Add("start %s to check the new path works", r.Name)
	if !rename && !o.KeepSource {
		p.AddIrreversible("delete %s", source)
	}

	p.Warn("this rewrites the WSL registry entry for the distribution",
		"everything is put back if the distribution does not start from the new location")
	if !rename && !o.KeepSource {
		p.Warn("the original is deleted only after the distribution has started from the copy",
			"pass --keep-source to leave it in place")
	}
	return p, nil
}

// checkDestination verifies a copy can land.
func checkDestination(e Env, source, dir string) error {
	vol, err := e.FS.Volume(dir)
	if err != nil {
		return fmt.Errorf("%w: %s could not be inspected: %v", ErrRefused, dir, err)
	}
	if !vol.SupportsVHDX() {
		return fmt.Errorf("%w: %s is %s, which caps a file at 4 GiB and has no sparse support; use an NTFS or ReFS volume",
			ErrRefused, dir, vol.FileSystem)
	}
	// Measured against what the file costs, not its virtual size: a 1 TiB
	// disk holding 12 GiB needs 12 GiB of room.
	size, err := e.FS.SizeOnDisk(source)
	if err != nil {
		return fmt.Errorf("%w: the size of %s could not be read: %v", ErrRefused, source, err)
	}
	if vol.FreeBytes < size+FreeSpaceMargin {
		return fmt.Errorf("%w: %s needs %s and %s has %s free",
			ErrRefused, BaseOf(source), FormatSize(size+FreeSpaceMargin), vol.Root, FormatSize(vol.FreeBytes))
	}
	return nil
}

// Move relocates a distribution disk and repoints the registration at it.
//
// The original is deleted last, and only after the distribution has been proved
// to start from the new copy. Everything before that point is undone if
// anything goes wrong.
func Move(ctx context.Context, e Env, r Registration, t MoveTarget, o MoveOptions, pr Progress) (MoveResult, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	source := r.VhdPath()
	res := MoveResult{Distro: r.Name, VhdPath: t.Path, KeptSource: o.KeepSource}
	if size, err := e.FS.SizeOnDisk(source); err == nil {
		res.SizeOnDisk = &size
	}

	same, err := e.FS.SameVolume(source, t.Dir)
	if err != nil {
		same = false
	}
	res.SameVolume = same
	res.Renamed = same && !o.KeepSource

	var undo UndoStack
	fail := func(err error) (MoveResult, error) {
		if uerr := undo.Unwind(); uerr != nil {
			return res, errors.Join(err, fmt.Errorf("and putting things back did not complete: %w", uerr))
		}
		return res, err
	}

	if err := e.FS.MkdirAll(t.Dir); err != nil {
		return res, err
	}

	if res.Renamed {
		pr.Step(fmt.Sprintf("move %s to %s", source, t.Path))
		if err := e.FS.Rename(source, t.Path); err != nil {
			return res, err
		}
		// Move it back, not delete it: the file at the target is the
		// original, and deleting it would destroy the distribution.
		undo.Push("move the disk back", func() error { return e.FS.Rename(t.Path, source) })
	} else {
		pr.Step(fmt.Sprintf("copy %s to %s", source, t.Path))
		if err := e.FS.CopySparse(source, t.Path, pr.Fraction); err != nil {
			return res, err
		}
		undo.Push("remove the copy", func() error { return e.FS.Remove(t.Path) })
	}

	intendedBase := WithSamePrefixAs(r.BasePath, t.Dir)
	res.BasePath = intendedBase
	pr.Step(fmt.Sprintf("point %s at %s", r.Name, t.Path))
	if err := repoint(e, r, t.Path, intendedBase, &undo); err != nil {
		return fail(err)
	}

	pr.Step(fmt.Sprintf("start %s to check the new path works", r.Name))
	if err := start(ctx, e, r.Name); err != nil {
		return fail(fmt.Errorf("%w from %s: %v. Everything has been put back and the original disk is untouched", ErrSmokeTest, t.Path, err))
	}

	// Past this point nothing is undone: the distribution has started from
	// the new location, so the original is no longer the one in use.
	if !res.Renamed && !o.KeepSource {
		pr.Step(fmt.Sprintf("delete %s", source))
		if err := e.FS.Remove(source); err != nil {
			// The move worked. The leftover did not go away, which is
			// worth saying and is not worth undoing a good move for.
			return res, fmt.Errorf("disk: %s was moved to %s, but the original at %s could not be deleted: %w",
				r.Name, t.Path, source, err)
		}
	}
	return res, nil
}

// MoveJSON is the object printed for --json.
func MoveJSON(res MoveResult) map[string]any {
	o := map[string]any{
		"distribution": res.Distro,
		"vhdx_path":    res.VhdPath,
		"base_path":    res.BasePath,
		"moved":        true,
		"renamed":      res.Renamed,
		"same_volume":  res.SameVolume,
		"kept_source":  res.KeptSource,
	}
	putU64(o, "size_on_disk", res.SizeOnDisk)
	return o
}
