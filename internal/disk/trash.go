package disk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// `wsl --unregister` deletes the disk along with the registration, has no
// confirmation, and fires its notification only after the deletion, so nothing
// can intercept it. A wrapper is the only way to make it survivable.
//
// So: stop the distribution, write down everything the registration says, move
// the disk somewhere else, and only then unregister. WSL finds nothing left to
// delete, and the manifest holds what is needed to put it back.

// ManifestName is the file inside a trash folder that records the registration.
const ManifestName = "wslkit-trash.json"

// ManifestSchema identifies the format, so a later version can tell what it is
// reading before it tries to restore from it.
const ManifestSchema = "wslkit/disk-trash/v1"

// Manifest is everything needed to bring a distribution back.
type Manifest struct {
	Schema string `json:"schema"`
	// TrashedAt is when it was moved, which is what --older-than compares.
	TrashedAt time.Time `json:"trashed_at"`
	// WasDefault records that this was the default distribution, which is
	// stored on the Lxss key rather than on the distribution itself and
	// would otherwise be lost.
	WasDefault bool `json:"was_default"`
	// Registration is the Lxss key as it stood.
	Registration Registration `json:"registration"`
	// Files are the names moved into the trash folder, disk first.
	Files []string `json:"files"`
	// VhdFile is the disk itself, which is what an import points at.
	VhdFile string `json:"vhd_file"`
}

// TrashEntry is one trashed distribution as `trash --list` reports it.
type TrashEntry struct {
	GUID     string
	Dir      string
	Manifest Manifest
	// Bytes is what the folder occupies, when it could be measured.
	Bytes *uint64
	// Err explains a folder that could not be read, so a broken entry does
	// not hide the ones that are fine.
	Err error
}

// Name is the distribution the entry came from.
func (e TrashEntry) Name() string {
	if e.Manifest.Registration.Name != "" {
		return e.Manifest.Registration.Name
	}
	return e.GUID
}

// Age is how long the entry has been in the trash.
func (e TrashEntry) Age(now time.Time) time.Duration {
	if e.Manifest.TrashedAt.IsZero() {
		return 0
	}
	return now.Sub(e.Manifest.TrashedAt)
}

// Errors the trash commands report.
var (
	// ErrNotTrashed means no trashed distribution matched.
	ErrNotTrashed = errors.New("disk: nothing in the trash matches that name")
	// ErrAlreadyRegistered means a distribution of that name exists, so
	// restoring would collide with it.
	ErrAlreadyRegistered = errors.New("disk: a distribution of that name is already registered")
)

// PlanTrash describes moving a distribution to the trash.
func PlanTrash(e Env, r Registration, running bool, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		// A WSL 1 distribution keeps its files directly on NTFS. There is
		// no single disk to move aside, so the wrapper cannot protect it.
		return p, fmt.Errorf("%w: %s is WSL 1, whose files are directly on NTFS; wslkit cannot move them aside, so use wsl --unregister knowing it deletes them", ErrNotWSL2, r.Name)
	}
	path := r.VhdPath()
	if path == "" || !e.FS.Exists(path) {
		return p, fmt.Errorf("%w: the disk of %s is not where the registry says it is (%s), so there is nothing to move aside", ErrRefused, r.Name, path)
	}
	if !runningKnown {
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and its disk cannot be moved while it is", ErrRefused, r.Name)
	}

	if running {
		p.Add("stop %s and wait for its disk", r.Name)
	}
	p.AddUndoable("move the disk of %s into the trash", r.Name)
	p.AddIrreversible("unregister %s, which now has no disk left to delete", r.Name)
	p.Warn("the distribution is unregistered, and comes back with wslkit disk undelete "+r.Name,
		"nothing inside it is deleted; the disk is moved, not removed")
	return p, nil
}

// TrashOptions controls how the disk is freed before it is moved.
type TrashOptions struct {
	// Shutdown permits stopping every distribution, not just this one. The
	// utility VM keeps every disk open while any distribution runs, so this
	// is the only way through when something else is up.
	Shutdown bool
	// UnlockTimeout is how long to wait for the disk. Zero means the default.
	UnlockTimeout time.Duration
}

// Trash moves a distribution out of WSL without destroying it.
func Trash(ctx context.Context, e Env, r Registration, trashRoot string, o TrashOptions, pr Progress) (TrashEntry, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	entry := TrashEntry{GUID: r.GUID, Dir: joinWindows(trashRoot, r.GUID)}

	// Everything the registration says, captured before anything moves.
	full, err := readFullRegistration(e, r)
	if err != nil {
		return entry, err
	}
	def, _, err := e.Registry.DefaultDistribution()
	if err != nil {
		return entry, err
	}

	man := Manifest{
		Schema:       ManifestSchema,
		TrashedAt:    e.Clock.Now().UTC(),
		WasDefault:   strings.EqualFold(def, r.GUID),
		Registration: full,
		VhdFile:      r.VhdName(),
	}

	// Stopping the distribution is not enough. The utility VM holds its disk
	// open for about a minute afterwards, so moving it straight away fails
	// with a sharing violation.
	if o.Shutdown {
		pr.Step("shut WSL down so the disk is released")
		if err := e.Host.Shutdown(ctx); err != nil {
			return entry, err
		}
	} else {
		pr.Step(fmt.Sprintf("stop %s and wait for its disk", r.Name))
		if err := e.Host.Terminate(ctx, r.Name); err != nil {
			return entry, err
		}
	}
	if err := WaitForDisk(ctx, e, r, r.VhdPath(), CompactOptions{Shutdown: o.Shutdown, UnlockTimeout: o.UnlockTimeout}, pr); err != nil {
		return entry, err
	}

	if err := e.FS.MkdirAll(entry.Dir); err != nil {
		return entry, err
	}
	// Anything that goes wrong from here leaves the trash folder empty, and
	// an empty folder shows up in the listing as an entry with no manifest.
	// Remove it rather than leave a phantom behind.
	cleanDir := func() {
		if items, err := e.FS.List(entry.Dir, "*"); err == nil && len(items) == 0 {
			_ = e.FS.RemoveDir(entry.Dir)
		}
	}

	var undo UndoStack
	pr.Step(fmt.Sprintf("move the disk of %s into the trash", r.Name))
	moved, err := moveIntoTrash(e, r, entry.Dir, &undo)
	if err != nil {
		uerr := undo.Unwind()
		cleanDir()
		if uerr != nil {
			return entry, errors.Join(err, fmt.Errorf("and putting it back did not complete: %w", uerr))
		}
		return entry, err
	}
	man.Files = moved

	// Written before the unregister: if writing it fails there is still a
	// registration pointing at a disk that has moved, which relink repairs.
	// Written afterwards, a failure would leave an unrecoverable folder.
	if err := writeManifest(e, entry.Dir, man); err != nil {
		uerr := undo.Unwind()
		cleanDir()
		if uerr != nil {
			return entry, errors.Join(err, fmt.Errorf("and putting the disk back did not complete: %w", uerr))
		}
		return entry, err
	}
	entry.Manifest = man

	pr.Step(fmt.Sprintf("unregister %s", r.Name))
	if err := e.Host.Unregister(ctx, r.Name); err != nil {
		// The registration still points at where the disk was, so putting
		// the disk back leaves the machine exactly as it was found.
		uerr := undo.Unwind()
		_ = e.FS.Remove(joinWindows(entry.Dir, ManifestName))
		cleanDir()
		if uerr != nil {
			return entry, errors.Join(err, fmt.Errorf("and putting the disk back did not complete: %w", uerr))
		}
		return entry, err
	}
	return entry, nil
}

// readFullRegistration re-reads every value, since the listing only carries the
// ones the other commands need.
func readFullRegistration(e Env, r Registration) (Registration, error) {
	out := r
	for _, f := range []struct {
		name string
		set  func(string)
	}{
		{"DistributionName", func(v string) { out.Name = v }},
		{"BasePath", func(v string) { out.BasePath = v }},
		{"VhdFileName", func(v string) { out.VhdFileName = v }},
		{"Flavor", func(v string) { out.Flavor = v }},
		{"OsVersion", func(v string) { out.OsVersion = v }},
		{"ShortcutPath", func(v string) { out.ShortcutPath = v }},
		{"TerminalProfilePath", func(v string) { out.TerminalProfilePath = v }},
	} {
		v, present, err := e.Registry.ReadString(r.GUID, f.name)
		if err != nil {
			return out, err
		}
		if present {
			f.set(v)
		}
	}
	for _, f := range []struct {
		name string
		set  func(uint32)
	}{
		{"Version", func(v uint32) { out.Version = int(v) }},
		{"State", func(v uint32) { out.State = int(v) }},
		{"Flags", func(v uint32) { out.Flags = int(v) }},
		{"DefaultUid", func(v uint32) { out.DefaultUID = int(v) }},
		{"RunOOBE", func(v uint32) { out.RunOOBE = int(v) }},
		{"Modern", func(v uint32) { out.Modern = int(v) }},
	} {
		v, present, err := e.Registry.ReadDWORD(r.GUID, f.name)
		if err != nil {
			return out, err
		}
		if present {
			f.set(v)
		}
	}
	return out, nil
}

// moveIntoTrash moves the disk, and any sibling WSL left beside it, into the
// trash folder.
//
// A rename is used wherever possible: it cannot half-succeed, and the trash is
// normally on the same volume as the disk.
func moveIntoTrash(e Env, r Registration, dir string, undo *UndoStack) ([]string, error) {
	source := r.VhdPath()
	var moved []string

	move := func(from, name string) error {
		to := joinWindows(dir, name)
		same, err := e.FS.SameVolume(from, dir)
		if err != nil {
			same = false
		}
		if same {
			if err := e.FS.Rename(from, to); err != nil {
				return err
			}
			undo.Push("move "+name+" back", func() error { return e.FS.Rename(to, from) })
		} else {
			if err := e.FS.CopySparse(from, to, nil); err != nil {
				return err
			}
			undo.Push("remove the copy of "+name, func() error { return e.FS.Remove(to) })
			if err := e.FS.Remove(from); err != nil {
				return err
			}
		}
		moved = append(moved, name)
		return nil
	}

	if err := move(source, r.VhdName()); err != nil {
		return moved, err
	}

	// Anything else WSL keeps beside the disk goes too, or unregistering
	// leaves it orphaned in a directory nothing points at.
	entries, err := e.FS.List(r.Base(), "*")
	if err != nil {
		// The disk is what matters; a base path that cannot be listed is
		// not a reason to stop.
		return moved, nil
	}
	for _, item := range entries {
		if item.IsDir {
			continue
		}
		name := BaseOf(item.Path)
		if strings.EqualFold(name, r.VhdName()) {
			continue
		}
		if err := move(item.Path, name); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

func writeManifest(e Env, dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("disk: building the trash manifest: %w", err)
	}
	return e.FS.WriteFile(joinWindows(dir, ManifestName), append(b, '\n'))
}

// ReadManifest loads one trash folder's manifest.
func ReadManifest(e Env, dir string) (Manifest, error) {
	var m Manifest
	b, err := e.FS.ReadFile(joinWindows(dir, ManifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("disk: %s is not a readable trash manifest: %w", joinWindows(dir, ManifestName), err)
	}
	if m.Schema != ManifestSchema {
		return m, fmt.Errorf("disk: %s was written by a different version of wslkit (%q)", joinWindows(dir, ManifestName), m.Schema)
	}
	return m, nil
}

// ListTrash reports what is in the trash, newest first.
func ListTrash(e Env, trashRoot string) ([]TrashEntry, error) {
	items, err := e.FS.List(trashRoot, "*")
	if err != nil {
		// An absent trash folder is an empty trash, not a failure.
		return nil, nil
	}
	var out []TrashEntry
	for _, item := range items {
		if !item.IsDir {
			continue
		}
		entry := TrashEntry{GUID: BaseOf(item.Path), Dir: item.Path}
		man, err := ReadManifest(e, item.Path)
		if err != nil {
			// Reported rather than hidden: a folder wslkit cannot read
			// is still taking up space the user may want back.
			entry.Err = err
		} else {
			entry.Manifest = man
		}
		if n, err := dirSize(e, item.Path); err == nil {
			entry.Bytes = &n
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Manifest.TrashedAt.After(out[j].Manifest.TrashedAt)
	})
	return out, nil
}

func dirSize(e Env, dir string) (uint64, error) {
	items, err := e.FS.List(dir, "*")
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, item := range items {
		if item.IsDir {
			continue
		}
		if n, err := e.FS.SizeOnDisk(item.Path); err == nil {
			total += n
		}
	}
	return total, nil
}

// FindTrashed picks one entry by distribution name or GUID.
func FindTrashed(entries []TrashEntry, name string) (TrashEntry, error) {
	var hits []TrashEntry
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) || strings.EqualFold(entry.GUID, name) {
			hits = append(hits, entry)
		}
	}
	switch len(hits) {
	case 0:
		return TrashEntry{}, fmt.Errorf("%w: %q", ErrNotTrashed, name)
	case 1:
		return hits[0], nil
	default:
		// The same distribution trashed twice. The GUID is what tells
		// them apart, so say so rather than restoring an arbitrary one.
		var guids []string
		for _, h := range hits {
			guids = append(guids, h.GUID)
		}
		return TrashEntry{}, fmt.Errorf("%w: %q was trashed more than once; name one of %s",
			ErrAmbiguous, name, strings.Join(guids, ", "))
	}
}

// PlanUndelete describes restoring a trashed distribution.
func PlanUndelete(entry TrashEntry, registered []Registration) (Plan, error) {
	name := entry.Manifest.Registration.Name
	p := Plan{Subject: name, SubjectKey: "distribution"}
	if entry.Err != nil {
		return p, fmt.Errorf("%w: %v", ErrRefused, entry.Err)
	}
	if name == "" {
		return p, fmt.Errorf("%w: the manifest in %s does not say what the distribution was called", ErrRefused, entry.Dir)
	}
	for _, r := range registered {
		if strings.EqualFold(r.Name, name) {
			return p, fmt.Errorf("%w: %s. Rename or remove it first", ErrAlreadyRegistered, name)
		}
	}
	p.AddUndoable("register %s from %s", name, joinWindows(entry.Dir, entry.Manifest.VhdFile))
	p.Add("restore the settings it had: default user, flags%s", defaultSuffix(entry.Manifest))
	p.Warn("the disk stays in the trash folder and is registered from there",
		"move it back with wslkit disk move "+name+" <directory> once you are happy")
	return p, nil
}

func defaultSuffix(m Manifest) string {
	if m.WasDefault {
		return ", and its place as the default distribution"
	}
	return ""
}

// Undelete brings a trashed distribution back.
//
// The disk is registered where it lies rather than copied back: copying tens of
// gibibytes to prove a restore worked is not a reasonable default, and
// `wslkit disk move` already exists for putting it somewhere permanent.
func Undelete(ctx context.Context, e Env, entry TrashEntry, pr Progress) (Registration, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	man := entry.Manifest
	name := man.Registration.Name
	vhd := joinWindows(entry.Dir, man.VhdFile)
	if !e.FS.Exists(vhd) {
		return Registration{}, fmt.Errorf("%w: the disk %s named by the manifest is not there", ErrRefused, vhd)
	}

	pr.Step(fmt.Sprintf("register %s from %s", name, vhd))
	if err := e.Host.ImportInPlace(ctx, name, vhd); err != nil {
		return Registration{}, err
	}

	// The import makes a new registration with a new GUID, so the old values
	// are written onto whatever WSL just created.
	list, _, err := e.Registry.Distros()
	if err != nil {
		return Registration{}, err
	}
	fresh, err := Resolve(list, name)
	if err != nil {
		return Registration{}, fmt.Errorf("disk: %s was imported but cannot be found in the registry: %w", name, err)
	}

	pr.Step("restore the settings it had")
	if err := restoreSettings(e, fresh.GUID, man); err != nil {
		// The distribution is back and usable; only its settings are not.
		return fresh, fmt.Errorf("disk: %s was restored, but not all of its settings could be put back: %w", name, err)
	}
	return fresh, nil
}

// restoreSettings puts back the values an import does not carry over.
func restoreSettings(e Env, guid string, m Manifest) error {
	var failures []error
	for _, f := range []struct {
		name  string
		value int
	}{
		{"DefaultUid", m.Registration.DefaultUID},
		{"Flags", m.Registration.Flags},
	} {
		if f.value < 0 {
			continue
		}
		if err := e.Registry.WriteDWORD(guid, f.name, uint32(f.value)); err != nil {
			failures = append(failures, err)
		}
	}
	if m.WasDefault {
		if err := e.Registry.SetDefaultDistribution(guid); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Purge deletes trash folders, returning what went.
func Purge(e Env, entries []TrashEntry) []DeleteResult {
	out := make([]DeleteResult, 0, len(entries))
	for _, entry := range entries {
		res := DeleteResult{Path: entry.Dir}
		items, err := e.FS.List(entry.Dir, "*")
		if err != nil {
			res.Err = err
			out = append(out, res)
			continue
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
			res.Err = err
		} else {
			if err := e.FS.RemoveDir(entry.Dir); err != nil {
				res.Err = err
			} else {
				res.Deleted = true
			}
		}
		out = append(out, res)
	}
	return out
}

// OlderThan filters entries by age.
func OlderThan(entries []TrashEntry, age time.Duration, now time.Time) []TrashEntry {
	if age <= 0 {
		return entries
	}
	var out []TrashEntry
	for _, entry := range entries {
		// An entry with no readable date is never purged by age: the
		// tool cannot say how old it is, and guessing would delete a
		// distribution.
		if entry.Manifest.TrashedAt.IsZero() {
			continue
		}
		if entry.Age(now) >= age {
			out = append(out, entry)
		}
	}
	return out
}

// TotalTrashBytes sums what the trash occupies.
func TotalTrashBytes(entries []TrashEntry) uint64 {
	var total uint64
	for _, entry := range entries {
		if entry.Bytes != nil {
			total += *entry.Bytes
		}
	}
	return total
}

// RenderTrashList writes the human-readable listing.
func RenderTrashList(w io.Writer, entries []TrashEntry, now time.Time) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "the trash is empty")
		return
	}
	t := Table{Headers: []string{"NAME", "SIZE", "TRASHED", "GUID"}}
	for _, entry := range entries {
		when := "-"
		if !entry.Manifest.TrashedAt.IsZero() {
			when = humanAge(entry.Age(now)) + " ago"
		}
		name := entry.Name()
		if entry.Err != nil {
			name = entry.GUID + " (unreadable)"
		}
		t.Rows = append(t.Rows, []string{name, sizeCell(entry.Bytes), when, entry.GUID})
	}
	fmt.Fprint(w, t.String())
	fmt.Fprintf(w, "\n%s in %d trashed distribution(s)\n", FormatSize(TotalTrashBytes(entries)), len(entries))
	fmt.Fprintln(w, "bring one back with wslkit disk undelete <name>, or free the space with wslkit disk trash --purge")
	for _, entry := range entries {
		if entry.Err != nil {
			fmt.Fprintf(w, "note: %s could not be read: %v\n", entry.Dir, entry.Err)
		}
	}
}

// humanAge renders a duration the way someone deciding whether to purge reads
// it: in the largest unit that still says something useful.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minute(s)", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hour(s)", int(d.Hours()))
	default:
		return fmt.Sprintf("%d day(s)", int(d.Hours()/24))
	}
}

// TrashJSON is the object printed per entry.
func TrashJSON(entry TrashEntry, now time.Time) map[string]any {
	o := map[string]any{
		"guid":         entry.GUID,
		"directory":    entry.Dir,
		"distribution": entry.Name(),
	}
	putU64(o, "size_on_disk", entry.Bytes)
	if !entry.Manifest.TrashedAt.IsZero() {
		o["trashed_at"] = entry.Manifest.TrashedAt.Format(time.RFC3339)
		o["age_seconds"] = int64(entry.Age(now).Seconds())
	}
	if entry.Manifest.WasDefault {
		o["was_default"] = true
	}
	if entry.Err != nil {
		o["error"] = entry.Err.Error()
	}
	return o
}

// ParseAge reads a duration written the way people write retention: 30d, 12h,
// 90m. Go's own parser has no day unit, and a day is the unit this is for.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%g", &days); err != nil {
			return 0, fmt.Errorf("%w: %q is not a number of days", ErrRefused, s)
		}
		if days < 0 {
			return 0, fmt.Errorf("%w: %q is negative", ErrRefused, s)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an age; write it like 30d, 12h or 90m", ErrRefused, s)
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: %q is negative", ErrRefused, s)
	}
	return d, nil
}
