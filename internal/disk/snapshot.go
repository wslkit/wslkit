package disk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// WSL has no checkpoints, and `wsl --export` is a tar of the files: slow, and
// it loses the disk's sparseness. A snapshot here is a copy of the disk file
// itself, with its holes kept, beside a manifest saying what it was (#85).
//
// Copies, not differencing disks: the service attaches whatever BasePath and
// VhdFileName name, and --manage --move, --resize, --set-sparse and --export
// all act on that one file, so a child disk would be broken silently by
// ordinary WSL commands.
//
// Snapshots are kept per distribution GUID, not per name, so a renamed
// distribution keeps its snapshots and a different distribution that happens
// to take the old name never gets them.

// SnapshotManifestName is the file beside each snapshot's disk.
const SnapshotManifestName = "wslkit-snapshot.json"

// SnapshotSchema identifies the manifest format.
const SnapshotSchema = "wslkit/disk-snapshot/v1"

// snapshotIDLayout names a snapshot by when it was taken, in UTC, so the ids
// sort in time order and mean the same on every machine.
const snapshotIDLayout = "20060102-150405"

// SnapshotManifest is everything a snapshot records about itself.
type SnapshotManifest struct {
	Schema       string    `json:"schema"`
	ID           string    `json:"id"`
	Label        string    `json:"label,omitempty"`
	Distribution string    `json:"distribution"`
	GUID         string    `json:"guid"`
	SourcePath   string    `json:"source_path"`
	VhdFile      string    `json:"vhd_file"`
	TakenAt      time.Time `json:"taken_at"`
	Runtime      string    `json:"runtime,omitempty"`
	FileSize     uint64    `json:"file_size"`
	SizeOnDisk   uint64    `json:"size_on_disk"`
}

// SnapshotEntry is one snapshot as the listing reports it.
type SnapshotEntry struct {
	Dir      string
	Manifest SnapshotManifest
	// Err explains a folder that could not be read, so one broken entry does
	// not hide the rest.
	Err error
}

// VhdPath is the snapshot's copy of the disk.
func (s SnapshotEntry) VhdPath() string { return joinWindows(s.Dir, s.Manifest.VhdFile) }

// SnapshotOptions controls taking one.
type SnapshotOptions struct {
	Label string
	// Runtime is the WSL version, recorded because a disk is only as
	// portable as the runtime that will boot it.
	Runtime string
	// Shutdown permits stopping every distribution to free the disk.
	Shutdown bool
}

// PlanSnapshot describes taking a snapshot.
func PlanSnapshot(e Env, r Registration, root string, o SnapshotOptions, running, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		return p, fmt.Errorf("%w: %s is WSL 1 and has no disk file to copy", ErrNotWSL2, r.Name)
	}
	source := r.VhdPath()
	if !e.FS.Exists(source) {
		return p, fmt.Errorf("%w: %s is not there. Run wslkit disk orphans to find it, then wslkit disk relink", ErrRefused, source)
	}
	if !runningKnown {
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and a disk copied while it is written to is not a snapshot", ErrRefused, r.Name)
	}
	if err := checkDestination(e, source, root); err != nil {
		return p, err
	}
	if running {
		p.Add("stop %s and wait for its disk", r.Name)
	}
	p.AddUndoable("copy %s into %s, keeping its holes", source, root)
	p.Add("write the snapshot's manifest")
	if running {
		p.Warn(r.Name+" is stopped for the copy and left stopped", "it starts again the next time you use it")
	}
	return p, nil
}

// TakeSnapshot copies the disk aside.
func TakeSnapshot(ctx context.Context, e Env, r Registration, root string, o SnapshotOptions, running bool, pr Progress) (SnapshotEntry, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	now := e.Clock.Now().UTC()
	id := now.Format(snapshotIDLayout)
	entry := SnapshotEntry{Dir: joinWindows(joinWindows(root, r.GUID), id)}
	source := r.VhdPath()

	if running || o.Shutdown {
		if err := stopForDisk(ctx, e, r, o.Shutdown, pr); err != nil {
			return entry, err
		}
	} else if err := WaitForDisk(ctx, e, r, source, CompactOptions{}, pr); err != nil {
		return entry, err
	}

	if e.FS.Exists(entry.Dir) {
		return entry, fmt.Errorf("%w: a snapshot of %s was taken in this same second; try again", ErrRefused, r.Name)
	}
	if err := e.FS.MkdirAll(entry.Dir); err != nil {
		return entry, err
	}
	dest := joinWindows(entry.Dir, r.VhdName())
	pr.Step(fmt.Sprintf("copy %s to %s", source, dest))
	if err := e.FS.CopySparse(source, dest, pr.Fraction); err != nil {
		_ = e.FS.Remove(dest)
		_ = e.FS.RemoveDir(entry.Dir)
		return entry, err
	}

	m := SnapshotManifest{
		Schema: SnapshotSchema, ID: id, Label: o.Label,
		Distribution: r.Name, GUID: r.GUID, SourcePath: source, VhdFile: r.VhdName(),
		TakenAt: now, Runtime: o.Runtime,
	}
	if n, err := e.FS.FileSize(dest); err == nil {
		m.FileSize = n
	}
	if n, err := e.FS.SizeOnDisk(dest); err == nil {
		m.SizeOnDisk = n
	}
	if err := writeSnapshotManifest(e, entry.Dir, m); err != nil {
		// A copy with no manifest cannot be restored by anything, so it is
		// not left behind looking like a snapshot.
		_ = e.FS.Remove(dest)
		_ = e.FS.RemoveDir(entry.Dir)
		return entry, err
	}
	entry.Manifest = m
	return entry, nil
}

// stopForDisk stops the distribution, or all of WSL, and waits until the
// utility VM lets go of the disk.
func stopForDisk(ctx context.Context, e Env, r Registration, shutdown bool, pr Progress) error {
	if shutdown {
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
	return WaitForDisk(ctx, e, r, r.VhdPath(), CompactOptions{Shutdown: shutdown}, pr)
}

func writeSnapshotManifest(e Env, dir string, m SnapshotManifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("disk: building the snapshot manifest: %w", err)
	}
	return e.FS.WriteFile(joinWindows(dir, SnapshotManifestName), append(b, '\n'))
}

func readSnapshotManifest(e Env, dir string) (SnapshotManifest, error) {
	var m SnapshotManifest
	b, err := e.FS.ReadFile(joinWindows(dir, SnapshotManifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("disk: %s is not a readable snapshot manifest: %w", joinWindows(dir, SnapshotManifestName), err)
	}
	if m.Schema != SnapshotSchema {
		return m, fmt.Errorf("disk: %s was written by a different version of wslkit (%q)", joinWindows(dir, SnapshotManifestName), m.Schema)
	}
	return m, nil
}

// ListSnapshots reports every snapshot, oldest first.
func ListSnapshots(e Env, root string) []SnapshotEntry {
	var out []SnapshotEntry
	guids, err := e.FS.List(root, "*")
	if err != nil {
		// No snapshot folder yet is no snapshots, not a failure.
		return nil
	}
	for _, g := range guids {
		if !g.IsDir {
			continue
		}
		ids, err := e.FS.List(g.Path, "*")
		if err != nil {
			continue
		}
		for _, item := range ids {
			if !item.IsDir {
				continue
			}
			entry := SnapshotEntry{Dir: item.Path}
			if m, err := readSnapshotManifest(e, item.Path); err != nil {
				entry.Err = err
				entry.Manifest.ID = BaseOf(item.Path)
				entry.Manifest.GUID = BaseOf(g.Path)
			} else {
				entry.Manifest = m
			}
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

// ErrNoSnapshot means no snapshot matched.
var ErrNoSnapshot = errors.New("disk: no such snapshot")

// SnapshotsOf picks a distribution's snapshots, by GUID.
func SnapshotsOf(all []SnapshotEntry, r Registration) []SnapshotEntry {
	var out []SnapshotEntry
	for _, s := range all {
		if strings.EqualFold(s.Manifest.GUID, r.GUID) {
			out = append(out, s)
		}
	}
	return out
}

// FindSnapshot picks one of a distribution's snapshots by id, by label, or
// "latest".
func FindSnapshot(all []SnapshotEntry, r Registration, id string) (SnapshotEntry, error) {
	mine := SnapshotsOf(all, r)
	if len(mine) == 0 {
		return SnapshotEntry{}, fmt.Errorf("%w: %s has none. Snapshots are kept by distribution GUID, so one taken before a rebuild or an unregister belongs to the old GUID", ErrNoSnapshot, r.Name)
	}
	if strings.EqualFold(id, "latest") {
		for i := len(mine) - 1; i >= 0; i-- {
			if mine[i].Err == nil {
				return mine[i], nil
			}
		}
	}
	for _, s := range mine {
		if s.Manifest.ID == id || (s.Manifest.Label != "" && s.Manifest.Label == id) {
			return s, nil
		}
	}
	var ids []string
	for _, s := range mine {
		ids = append(ids, s.Manifest.ID)
	}
	return SnapshotEntry{}, fmt.Errorf("%w: %s has no snapshot %q (it has %s)", ErrNoSnapshot, r.Name, id, strings.Join(ids, ", "))
}

// RemoveSnapshot deletes one snapshot's files and its folder.
func RemoveSnapshot(e Env, s SnapshotEntry) error {
	items, err := e.FS.List(s.Dir, "*")
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if item.IsDir {
			continue
		}
		if err := e.FS.Remove(item.Path); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	return e.FS.RemoveDir(s.Dir)
}

// beforeRestoreSuffix marks the live disk while a restore is in progress.
const beforeRestoreSuffix = ".wslkit-before-restore"

// PlanRestore describes putting a snapshot back.
func PlanRestore(e Env, r Registration, s SnapshotEntry, running, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if s.Err != nil {
		return p, fmt.Errorf("%w: %v", ErrRefused, s.Err)
	}
	if !strings.EqualFold(s.Manifest.GUID, r.GUID) {
		return p, fmt.Errorf("%w: snapshot %s is of %s (%s), not of %s", ErrRefused, s.Manifest.ID, s.Manifest.Distribution, s.Manifest.GUID, r.Name)
	}
	if r.Version != 2 || r.VhdPath() == "" {
		return p, fmt.Errorf("%w: %s has no disk file to restore onto", ErrNotWSL2, r.Name)
	}
	if !e.FS.Exists(s.VhdPath()) {
		return p, fmt.Errorf("%w: the snapshot's disk %s is not there", ErrRefused, s.VhdPath())
	}
	if !runningKnown {
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and its disk cannot be replaced while it is", ErrRefused, r.Name)
	}
	live := r.VhdPath()
	if e.FS.Exists(live + beforeRestoreSuffix) {
		return p, fmt.Errorf("%w: %s is left over from an earlier restore that did not finish; move it aside first", ErrRefused, live+beforeRestoreSuffix)
	}
	// The current disk stays where it is until the restore has proved
	// itself, so the copy needs room of its own.
	if err := checkDestination(e, s.VhdPath(), r.Base()); err != nil {
		return p, err
	}
	if running {
		p.Add("stop %s and wait for its disk", r.Name)
	}
	if e.FS.Exists(live) {
		p.AddUndoable("move the current disk aside, to %s", BaseOf(live+beforeRestoreSuffix))
	}
	p.AddUndoable("copy snapshot %s into place", s.Manifest.ID)
	p.Add("start %s to check the restored disk boots", r.Name)
	if e.FS.Exists(live) {
		p.Add("keep the disk it replaced as a snapshot of its own")
		p.Warn("everything written to "+r.Name+" since "+s.Manifest.TakenAt.Local().Format("2006-01-02 15:04")+" is replaced",
			"the disk it replaces is kept as a snapshot, and wslkit doctor undo restores that one")
	}
	return p, nil
}

// RestoreResult is the outcome.
type RestoreResult struct {
	Distro   string
	Snapshot string
	// Before is the snapshot made of the disk that was replaced, empty when
	// there was no disk or it could not be kept.
	Before string
	// BeforeErr says why the replaced disk could not be filed as a
	// snapshot; it is then still beside the live disk.
	BeforeErr error
}

// Restore puts a snapshot back. The current disk is moved aside, not deleted,
// until the restored copy has booted; if it does not, everything is put back.
//
// shutdown permits stopping every distribution to free the disk: once a
// distribution has run, the utility VM holds its disk while any other one runs.
func Restore(ctx context.Context, e Env, r Registration, s SnapshotEntry, root string, running, shutdown bool, pr Progress) (RestoreResult, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	res := RestoreResult{Distro: r.Name, Snapshot: s.Manifest.ID}
	live := r.VhdPath()
	aside := live + beforeRestoreSuffix

	if running || shutdown {
		if err := stopForDisk(ctx, e, r, shutdown, pr); err != nil {
			return res, err
		}
	} else if e.FS.Exists(live) {
		if err := WaitForDisk(ctx, e, r, live, CompactOptions{}, pr); err != nil {
			return res, err
		}
	}

	var undo UndoStack
	fail := func(err error) (RestoreResult, error) {
		if uerr := undo.Unwind(); uerr != nil {
			return res, errors.Join(err, fmt.Errorf("and putting things back did not complete: %w", uerr))
		}
		return res, err
	}

	hadLive := e.FS.Exists(live)
	if hadLive {
		pr.Step("move the current disk aside")
		if err := e.FS.Rename(live, aside); err != nil {
			return res, err
		}
		undo.Push("move the current disk back", func() error { return e.FS.Rename(aside, live) })
	}
	pr.Step(fmt.Sprintf("copy snapshot %s into place", s.Manifest.ID))
	if err := e.FS.CopySparse(s.VhdPath(), live, pr.Fraction); err != nil {
		_ = e.FS.Remove(live)
		return fail(err)
	}
	undo.Push("remove the restored copy", func() error { return e.FS.Remove(live) })

	pr.Step(fmt.Sprintf("start %s to check the restored disk boots", r.Name))
	if err := start(ctx, e, r.Name); err != nil {
		// The disk has to be free again before the undo can move files.
		_ = e.Host.Terminate(ctx, r.Name)
		_ = WaitForDisk(ctx, e, r, live, CompactOptions{}, DiscardProgress{})
		return fail(fmt.Errorf("%w from snapshot %s: %v. The disk it was replacing has been put back", ErrSmokeTest, s.Manifest.ID, err))
	}
	if !hadLive {
		return res, nil
	}

	// The restore worked. The disk it replaced is filed as a snapshot of its
	// own, so the restore is itself undoable and nothing was thrown away.
	pr.Step("keep the disk it replaced as a snapshot")
	before, err := fileAsSnapshot(e, r, root, aside, "before restoring "+s.Manifest.ID)
	if err != nil {
		res.BeforeErr = fmt.Errorf("the disk it replaced is still at %s: %w", aside, err)
		return res, nil
	}
	res.Before = before.Manifest.ID
	return res, nil
}

// fileAsSnapshot moves a disk file into the snapshot store with a manifest.
func fileAsSnapshot(e Env, r Registration, root, path, label string) (SnapshotEntry, error) {
	now := e.Clock.Now().UTC()
	id := now.Format(snapshotIDLayout)
	entry := SnapshotEntry{Dir: joinWindows(joinWindows(root, r.GUID), id)}
	if e.FS.Exists(entry.Dir) {
		// The snapshot being restored may have been taken this same second
		// in a test; never overwrite one.
		id += "-1"
		entry.Dir = joinWindows(joinWindows(root, r.GUID), id)
	}
	if err := e.FS.MkdirAll(entry.Dir); err != nil {
		return entry, err
	}
	dest := joinWindows(entry.Dir, r.VhdName())
	same, err := e.FS.SameVolume(path, entry.Dir)
	if err != nil {
		same = false
	}
	if same {
		if err := e.FS.Rename(path, dest); err != nil {
			return entry, err
		}
	} else {
		if err := e.FS.CopySparse(path, dest, nil); err != nil {
			_ = e.FS.Remove(dest)
			return entry, err
		}
		if err := e.FS.Remove(path); err != nil {
			return entry, err
		}
	}
	m := SnapshotManifest{
		Schema: SnapshotSchema, ID: id, Label: label,
		Distribution: r.Name, GUID: r.GUID, SourcePath: r.VhdPath(), VhdFile: r.VhdName(), TakenAt: now,
	}
	if n, err := e.FS.FileSize(dest); err == nil {
		m.FileSize = n
	}
	if n, err := e.FS.SizeOnDisk(dest); err == nil {
		m.SizeOnDisk = n
	}
	entry.Manifest = m
	return entry, writeSnapshotManifest(e, entry.Dir, m)
}

// SnapshotJSON is the object printed per snapshot.
func SnapshotJSON(s SnapshotEntry) map[string]any {
	o := map[string]any{
		"id":           s.Manifest.ID,
		"distribution": s.Manifest.Distribution,
		"guid":         s.Manifest.GUID,
		"path":         s.VhdPath(),
		"taken_at":     s.Manifest.TakenAt,
		"size_on_disk": s.Manifest.SizeOnDisk,
	}
	if s.Manifest.Label != "" {
		o["label"] = s.Manifest.Label
	}
	if s.Manifest.Runtime != "" {
		o["runtime"] = s.Manifest.Runtime
	}
	if s.Err != nil {
		o["error"] = s.Err.Error()
	}
	return o
}
