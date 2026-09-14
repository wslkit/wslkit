package disk

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Compaction works in whole blocks of the virtual disk. Free space scattered
// through a filesystem in small holes leaves most of those blocks partly used,
// so a disk that is half empty inside can reclaim almost nothing, and the
// command that reclaims nothing is the one that makes people give up on it.
//
// Export and import rebuilds the filesystem from its contents instead. Every
// file is written afresh into a new disk, in order, and the holes are gone. It
// is the one thing that reliably shrinks a disk that compaction cannot.
//
// What it loses, done by hand, is the registration: `wsl --import` makes a new
// GUID and drops the default user, the flags and the default-distribution
// marker. Putting those back is the whole of what this adds, and it is the same
// work `disk trash` already does, done in the other direction.

// RebuildOptions controls a rebuild.
type RebuildOptions struct {
	// WorkDir is where the intermediate archive is written. Empty means
	// beside the disk, which is the volume most likely to have room for it
	// and is on the same drive, so nothing crosses a network.
	WorkDir string
	// KeepArchive leaves the archive in place afterwards. It is a complete
	// backup of the distribution, and someone about to do this to a machine
	// they care about may well want to keep one.
	KeepArchive bool
	// Restart starts the distribution again afterwards if it was running.
	Restart bool
	// ExportTimeout and ImportTimeout bound the two long steps. A large
	// distribution takes minutes; an hour means something is wrong.
	ExportTimeout time.Duration
	ImportTimeout time.Duration
	// RunningBefore is the set of distributions that were up beforehand.
	RunningBefore map[string]bool
}

// DefaultRebuildTimeout bounds an export or an import. Tens of gigabytes take
// minutes over a real disk; an hour is long enough that nothing legitimate
// takes it and short enough that a wedged wsl.exe does not hold a terminal for
// a working day.
const DefaultRebuildTimeout = time.Hour

// RebuildResult is the outcome.
type RebuildResult struct {
	Distro string
	// Before and After are the size on disk either side.
	Before, After *uint64
	// Archive is where the intermediate archive was written, and where it
	// still is when it was kept or when the rebuild failed after it was
	// made.
	Archive string
	// ArchiveKept says the archive is still there.
	ArchiveKept bool
	// VhdPath is the disk after the rebuild, which is where the import put
	// it and need not be where the old one was.
	VhdPath string
	// Restored lists the settings put back onto the new registration.
	Restored []string
}

// Reclaimed is what the rebuild gave back, floored at zero.
func (r RebuildResult) Reclaimed() *uint64 {
	if r.Before == nil || r.After == nil {
		return nil
	}
	n := uint64(0)
	if *r.Before > *r.After {
		n = *r.Before - *r.After
	}
	return &n
}

// PlanRebuild describes what a rebuild would do, and refuses the cases it
// cannot do safely.
//
// The refusals are the point of planning separately: everything here is checked
// before the distribution is stopped, so a refusal costs nothing.
func PlanRebuild(e Env, r Registration, o RebuildOptions, running bool, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		return p, fmt.Errorf("%w: %s is a WSL 1 distribution, which has no disk to rebuild", ErrRefused, r.Name)
	}
	source := r.VhdPath()
	if source == "" {
		return p, fmt.Errorf("%w: %s has no disk recorded in the registry", ErrRefused, r.Name)
	}
	if !e.FS.Exists(source) {
		return p, fmt.Errorf("%w: %s is not there. Run wslkit disk orphans to find it, then wslkit disk relink", ErrRefused, source)
	}
	if !runningKnown {
		// A rebuild unregisters and re-registers. Doing that without
		// knowing whether the distribution is in use is not a risk worth
		// taking for a command that can be run again in a moment.
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and a rebuild cannot be done safely while it might be", ErrRefused, r.Name)
	}

	archive := rebuildArchivePath(e, r, o)
	if e.FS.Exists(archive) {
		return p, fmt.Errorf("%w: %s already exists; move it aside or choose another --work-dir", ErrRefused, archive)
	}
	if err := checkArchiveRoom(e, source, archive); err != nil {
		return p, err
	}

	if running {
		p.Add("stop %s", r.Name)
	}
	p.Add("export %s to %s", r.Name, archive)
	// Nothing after this registers a rollback, and nothing could: once the
	// old disk is gone the archive is the only copy, and the way back is to
	// import it again rather than to undo anything.
	p.AddIrreversible("unregister %s, which deletes %s", r.Name, source)
	p.Add("import %s again from %s", r.Name, archive)
	p.Add("put back the default user, the flags and the default-distribution marker")
	p.Add("start %s to check it works", r.Name)
	if !o.KeepArchive {
		p.AddIrreversible("delete %s", archive)
	}

	p.Warn("the old disk is deleted, and the archive is what the distribution is rebuilt from",
		"the archive is kept wherever anything fails, with the command to recover from it")
	p.Warn("the distribution gets a new GUID",
		"anything that recorded the old one, such as a plugin registration or a script, will not find it")
	if !o.KeepArchive {
		p.Warn("the archive is deleted once the distribution has started again",
			"pass --keep-archive to keep it as a backup")
	}
	return p, nil
}

// rebuildArchivePath is where the intermediate archive goes.
func rebuildArchivePath(e Env, r Registration, o RebuildOptions) string {
	dir := o.WorkDir
	if dir == "" {
		dir = DirOf(r.VhdPath())
	}
	stamp := e.Clock.Now().UTC().Format("20060102-150405")
	return joinWindows(dir, fmt.Sprintf("wslkit-rebuild-%s-%s.tar", r.GUID, stamp))
}

// checkArchiveRoom refuses before anything is stopped if the archive will not
// fit. The archive holds the files, not the disk, so the disk's size on disk is
// a generous upper bound and a safe one to check against.
func checkArchiveRoom(e Env, source, archive string) error {
	size, err := e.FS.SizeOnDisk(source)
	if err != nil {
		return fmt.Errorf("%w: the size of %s could not be read: %v", ErrRefused, source, err)
	}
	vol, err := e.FS.Volume(DirOf(archive))
	if err != nil {
		return fmt.Errorf("%w: %s could not be inspected: %v", ErrRefused, DirOf(archive), err)
	}
	if vol.FreeBytes < size+FreeSpaceMargin {
		return fmt.Errorf("%w: the archive needs about %s and %s has %s free. Use --work-dir to write it somewhere else",
			ErrRefused, FormatSize(size+FreeSpaceMargin), vol.Root, FormatSize(vol.FreeBytes))
	}
	return nil
}

// Rebuild exports a distribution, re-imports it into a fresh disk, and puts the
// registration back.
//
// The archive is the safety net, and it is what makes the irreversible step
// acceptable: from the moment it exists until the distribution has started
// again, everything that goes wrong leaves the archive in place and says where
// it is.
func Rebuild(ctx context.Context, e Env, r Registration, o RebuildOptions, pr Progress) (RebuildResult, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	if o.ExportTimeout <= 0 {
		o.ExportTimeout = DefaultRebuildTimeout
	}
	if o.ImportTimeout <= 0 {
		o.ImportTimeout = DefaultRebuildTimeout
	}
	source := r.VhdPath()
	base := r.BasePath
	archive := rebuildArchivePath(e, r, o)
	res := RebuildResult{Distro: r.Name, Archive: archive}
	if n, err := e.FS.SizeOnDisk(source); err == nil {
		res.Before = &n
	}

	// Everything the import does not carry over, read before the
	// registration is destroyed.
	def, _, err := e.Registry.DefaultDistribution()
	if err != nil {
		return res, err
	}
	man := Manifest{
		Schema:       ManifestSchema,
		TrashedAt:    e.Clock.Now().UTC(),
		WasDefault:   def != "" && equalFoldASCII(def, r.GUID),
		Registration: r,
		VhdFile:      BaseOf(source),
	}

	// Only this distribution is stopped, and nothing waits for the utility VM
	// to let go of the file. A rebuild never opens the disk itself: wsl.exe
	// does the export, the unregister and the import, and it is entitled to
	// its own open handles. Waiting for the lock here would refuse the whole
	// operation whenever any other distribution happened to be running,
	// which is most of the time and none of its business.
	if o.RunningBefore[r.Name] {
		pr.Step(fmt.Sprintf("stop %s", r.Name))
		if err := e.Host.Terminate(ctx, r.Name); err != nil {
			return res, fmt.Errorf("disk: stopping %s: %w", r.Name, err)
		}
	}

	pr.Step(fmt.Sprintf("export %s to %s", r.Name, archive))
	if err := e.FS.MkdirAll(DirOf(archive)); err != nil {
		return res, err
	}
	if err := e.Host.Export(ctx, r.Name, archive, o.ExportTimeout); err != nil {
		// Nothing has been destroyed: the distribution is exactly as it
		// was, minus being stopped.
		_ = e.FS.Remove(archive)
		return res, fmt.Errorf("disk: exporting %s failed, and nothing was changed: %w", r.Name, err)
	}
	if !e.FS.Exists(archive) {
		return res, fmt.Errorf("disk: the export of %s reported success but %s is not there, so nothing was changed", r.Name, archive)
	}
	res.ArchiveKept = true

	// From here the archive is the only copy, and it is kept on every path
	// out of this function that does not end in a working distribution.
	pr.Step(fmt.Sprintf("unregister %s", r.Name))
	if err := e.Host.Unregister(ctx, r.Name); err != nil {
		return res, fmt.Errorf("disk: unregistering %s failed: %w. The archive is at %s and the distribution is untouched", r.Name, err, archive)
	}

	pr.Step(fmt.Sprintf("import %s from %s", r.Name, archive))
	if err := e.Host.ImportTar(ctx, r.Name, base, archive, o.ImportTimeout); err != nil {
		return res, fmt.Errorf("disk: %s was exported and unregistered, but importing it again failed: %w.\nThe archive is at %s. Recover with:\n  wsl --import %s %s %s --version 2",
			r.Name, err, archive, r.Name, base, archive)
	}

	list, _, err := e.Registry.Distros()
	if err != nil {
		return res, fmt.Errorf("disk: %s was imported but the registry could not be read back: %w", r.Name, err)
	}
	fresh, err := Resolve(list, r.Name)
	if err != nil {
		return res, fmt.Errorf("disk: %s was imported but cannot be found in the registry: %w", r.Name, err)
	}
	res.VhdPath = fresh.VhdPath()
	if n, err := e.FS.SizeOnDisk(res.VhdPath); err == nil {
		res.After = &n
	}

	pr.Step("put back the settings the import does not carry")
	if err := restoreSettings(e, fresh.GUID, man); err != nil {
		return res, fmt.Errorf("disk: %s was rebuilt, but not all of its settings could be put back: %w", r.Name, err)
	}
	res.Restored = restoredNames(man)

	pr.Step(fmt.Sprintf("start %s to check it works", r.Name))
	if err := start(ctx, e, r.Name); err != nil {
		return res, fmt.Errorf("%w after the rebuild: %v.\nThe archive is at %s, so the distribution can be built again from it", ErrSmokeTest, err, archive)
	}

	if !o.KeepArchive {
		pr.Step("delete " + archive)
		if err := e.FS.Remove(archive); err != nil {
			// The rebuild worked. A leftover archive is worth saying and
			// is not worth calling the rebuild a failure for.
			return res, fmt.Errorf("disk: %s was rebuilt, but the archive at %s could not be deleted: %w", r.Name, archive, err)
		}
		res.ArchiveKept = false
	}

	if !o.Restart || !o.RunningBefore[r.Name] {
		// The smoke test started it. Leaving it running when it was not
		// running before would be a side effect nobody asked for.
		if err := e.Host.Terminate(ctx, r.Name); err != nil {
			return res, nil
		}
	}
	return res, nil
}

// restoredNames lists what was put back, for the report.
func restoredNames(m Manifest) []string {
	var out []string
	if m.Registration.DefaultUID >= 0 {
		out = append(out, "default user")
	}
	if m.Registration.Flags >= 0 {
		out = append(out, "flags")
	}
	if m.WasDefault {
		out = append(out, "default distribution")
	}
	return out
}

// RenderRebuild writes the human-readable result.
func RenderRebuild(w io.Writer, res RebuildResult) {
	fmt.Fprintf(w, "%s rebuilt", res.Distro)
	if r := res.Reclaimed(); r != nil && res.Before != nil && res.After != nil {
		fmt.Fprintf(w, ": %s reclaimed (%s to %s)", FormatSize(*r), FormatSize(*res.Before), FormatSize(*res.After))
	}
	fmt.Fprintln(w)
	if len(res.Restored) > 0 {
		fmt.Fprintf(w, "  put back: %s\n", joinWords(res.Restored))
	}
	if res.VhdPath != "" {
		fmt.Fprintf(w, "  disk: %s\n", res.VhdPath)
	}
	if res.ArchiveKept {
		fmt.Fprintf(w, "  archive kept: %s\n", res.Archive)
	}
}

// RebuildJSON is the object printed for --json.
func RebuildJSON(res RebuildResult) map[string]any {
	m := map[string]any{
		"distribution": res.Distro,
		"rebuilt":      true,
		"archive":      res.Archive,
		"archive_kept": res.ArchiveKept,
	}
	if res.VhdPath != "" {
		m["vhdx_path"] = res.VhdPath
	}
	putU64(m, "size_on_disk_before", res.Before)
	putU64(m, "size_on_disk_after", res.After)
	putU64(m, "reclaimed", res.Reclaimed())
	if len(res.Restored) > 0 {
		m["restored"] = res.Restored
	}
	return m
}

// joinWords renders a list the way a sentence would.
func joinWords(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return fmt.Sprintf("%s and %s", joinCommas(items[:len(items)-1]), items[len(items)-1])
}

func joinCommas(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// equalFoldASCII compares two GUIDs without allocating.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
