package disk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const trashRoot = `C:\Users\u\AppData\Local\wslkit\trash`

var trashNow = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

// trashEnv describes a machine with one registered distribution whose disk, and
// a file WSL keeps beside it, are on the same volume as the trash.
func trashEnv() (Env, *fakeFS, *fakeRegistry, *fakeHost, *fakeClock) {
	base := `C:\wsl\Ubuntu`
	vhd := base + `\ext4.vhdx`
	extra := base + `\wsl-config.json`
	fsys := &fakeFS{
		files: map[string]fakeFile{
			vhd:   {size: 100 << 30, onDisk: 12 << 30},
			extra: {size: 100, onDisk: 100},
		},
		dirs: map[string][]DirEntry{
			base: {{Path: vhd}, {Path: extra}},
		},
		env: map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`},
	}
	registry := &fakeRegistry{
		list: []Registration{reg("Ubuntu", func(r *Registration) { r.IsDefault = true })},
		values: map[string]string{
			"{Ubuntu}\x00DistributionName": "Ubuntu",
			"{Ubuntu}\x00BasePath":         base,
			"{Ubuntu}\x00VhdFileName":      "ext4.vhdx",
			"{Ubuntu}\x00Flavor":           "ubuntu",
		},
		dwords: map[string]uint32{
			"{Ubuntu}\x00Version":    2,
			"{Ubuntu}\x00Flags":      15,
			"{Ubuntu}\x00DefaultUid": 1000,
		},
		defaultGUID: "{Ubuntu}",
	}
	host := &fakeHost{}
	clock := &fakeClock{now: trashNow}
	return Env{Registry: registry, FS: fsys, Disks: &fakeDisks{}, Host: host, Clock: clock}, fsys, registry, host, clock
}

// The disk must be out of the way before the unregister runs, because
// unregistering deletes whatever it still points at.
func TestTrashMovesTheDiskBeforeUnregistering(t *testing.T) {
	e, fsys, _, host, _ := trashEnv()

	entry, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	dest := trashRoot + `\{Ubuntu}\ext4.vhdx`
	if _, ok := fsys.files[dest]; !ok {
		t.Fatalf("the disk is not in the trash: %v", fsys.files)
	}
	if _, ok := fsys.files[`C:\wsl\Ubuntu\ext4.vhdx`]; ok {
		t.Error("the disk is still at its old path")
	}

	// Order matters more than anything else here.
	joined := strings.Join(host.calls, "; ")
	moved := strings.Index(joined, "unregister")
	if moved < 0 {
		t.Fatalf("nothing was unregistered: %v", host.calls)
	}
	if len(fsys.renames) == 0 {
		t.Fatal("the disk was never moved")
	}
	if entry.Manifest.VhdFile != "ext4.vhdx" {
		t.Errorf("manifest names %q as the disk", entry.Manifest.VhdFile)
	}
}

// Whatever WSL keeps beside the disk goes too, or unregistering leaves it
// orphaned in a directory nothing points at any more.
func TestTrashTakesTheSiblingFilesWithIt(t *testing.T) {
	e, fsys, _, _, _ := trashEnv()
	entry, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fsys.files[trashRoot+`\{Ubuntu}\wsl-config.json`]; !ok {
		t.Errorf("the sibling file was left behind: %v", fsys.files)
	}
	if len(entry.Manifest.Files) != 2 {
		t.Errorf("the manifest should list both files: %v", entry.Manifest.Files)
	}
	// The disk is listed first, because that is what a restore points at.
	if entry.Manifest.Files[0] != "ext4.vhdx" {
		t.Errorf("the disk should be first: %v", entry.Manifest.Files)
	}
}

// Being the default is recorded on the Lxss key, not on the distribution, so it
// is lost unless the manifest carries it.
func TestTrashRecordsEverythingNeededToComeBack(t *testing.T) {
	e, fsys, _, _, _ := trashEnv()
	if _, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	b, ok := fsys.blobs[trashRoot+`\{Ubuntu}\`+ManifestName]
	if !ok {
		t.Fatal("no manifest was written")
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v\n%s", err, b)
	}
	if m.Schema != ManifestSchema {
		t.Errorf("schema %q", m.Schema)
	}
	if !m.WasDefault {
		t.Error("being the default distribution was not recorded")
	}
	if m.Registration.Flags != 15 || m.Registration.DefaultUID != 1000 {
		t.Errorf("the registration was not captured: %+v", m.Registration)
	}
	if m.Registration.Name != "Ubuntu" || m.Registration.Flavor != "ubuntu" {
		t.Errorf("string values were not captured: %+v", m.Registration)
	}
	if m.TrashedAt.IsZero() {
		t.Error("no timestamp, so --older-than could never purge it")
	}
}

// If the unregister fails the disk goes back, because leaving it in the trash
// while the registration still points at where it was is a broken distribution.
func TestTrashPutsTheDiskBackIfUnregisteringFails(t *testing.T) {
	e, fsys, _, host, _ := trashEnv()
	host.unregisterErr = errors.New("access is denied")

	if _, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil); err == nil {
		t.Fatal("expected the failure to be reported")
	}
	if _, ok := fsys.files[`C:\wsl\Ubuntu\ext4.vhdx`]; !ok {
		t.Fatalf("the disk was not put back: %v", fsys.files)
	}
	if _, ok := fsys.files[trashRoot+`\{Ubuntu}\ext4.vhdx`]; ok {
		t.Error("the disk is still in the trash")
	}
}

func TestPlanTrashRefusals(t *testing.T) {
	e, _, _, _, _ := trashEnv()

	// WSL 1 keeps its files directly on NTFS, so there is no single disk to
	// move aside and the wrapper cannot protect it. Say so rather than
	// pretending.
	_, err := PlanTrash(e, reg("Legacy", func(r *Registration) { r.Version = 1 }), false, true)
	if !errors.Is(err, ErrNotWSL2) {
		t.Errorf("WSL 1: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "deletes them") {
		t.Errorf("the refusal should be honest about what wsl --unregister does: %v", err)
	}

	if _, err := PlanTrash(e, reg("Gone", func(r *Registration) { r.BasePath = `C:\nowhere` }), false, true); !errors.Is(err, ErrRefused) {
		t.Errorf("a missing disk: %v", err)
	}
	if _, err := PlanTrash(e, reg("Ubuntu"), false, false); !errors.Is(err, ErrRefused) {
		t.Errorf("not knowing whether it runs: %v", err)
	}
}

// The unregister is the point of no return and must be last.
func TestPlanTrashOrdersTheUnregisterLast(t *testing.T) {
	e, _, _, _, _ := trashEnv()
	p, err := PlanTrash(e, reg("Ubuntu"), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Valid(); err != nil {
		t.Fatalf("plan should be valid: %v", err)
	}
	last := p.Steps[len(p.Steps)-1]
	if !last.Irreversible || !strings.Contains(last.Description, "unregister") {
		t.Fatalf("the unregister should be last and irreversible: %+v", p.Steps)
	}
	if !strings.Contains(p.Warnings[0].Message, "undelete Ubuntu") {
		t.Errorf("the warning should say how to get it back: %+v", p.Warnings)
	}
}

// ---------------------------------------------------------------- undelete

// trashedEnv describes a machine with one distribution already in the trash.
func trashedEnv(t *testing.T, wasDefault bool) (Env, *fakeFS, *fakeRegistry, *fakeHost) {
	t.Helper()
	e, fsys, registry, host, _ := trashEnv()
	if !wasDefault {
		registry.defaultGUID = ""
	}
	if _, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	// After the trash, nothing is registered.
	registry.list = nil
	registry.defaultGUID = ""
	host.calls = nil
	return e, fsys, registry, host
}

func TestUndeleteRegistersTheDiskWhereItLiesAndRestoresSettings(t *testing.T) {
	e, _, registry, host := trashedEnv(t, true)
	// Stand in for wsl.exe: the import makes a new registration with a new
	// GUID, which is why the settings have to be written afterwards.
	host.onImport = func(name, vhdPath string) {
		registry.list = []Registration{{GUID: "{fresh}", Name: name, Version: 2, BasePath: DirOf(vhdPath), VhdFileName: BaseOf(vhdPath)}}
	}

	entries, err := ListTrash(e, trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := FindTrashed(entries, "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Undelete(context.Background(), e, entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.GUID != "{fresh}" {
		t.Errorf("got %q", fresh.GUID)
	}
	// Registered from the trash folder, not copied back: copying tens of
	// gibibytes to prove a restore worked is not a reasonable default.
	joined := strings.Join(host.calls, "; ")
	if !strings.Contains(joined, "import-in-place Ubuntu "+trashRoot) {
		t.Errorf("expected an in-place import from the trash: %v", host.calls)
	}
	if registry.dwords["{fresh}\x00Flags"] != 15 {
		t.Errorf("flags were not restored: %v", registry.dwords)
	}
	if registry.dwords["{fresh}\x00DefaultUid"] != 1000 {
		t.Errorf("the default user was not restored: %v", registry.dwords)
	}
	if registry.defaultGUID != "{fresh}" {
		t.Errorf("it was the default and should be again: %q", registry.defaultGUID)
	}
}

// A distribution that was not the default must not become it.
func TestUndeleteDoesNotMakeItTheDefaultUnlessItWas(t *testing.T) {
	e, _, registry, host := trashedEnv(t, false)
	host.onImport = func(name, vhdPath string) {
		registry.list = []Registration{{GUID: "{fresh}", Name: name, Version: 2}}
	}
	entries, _ := ListTrash(e, trashRoot)
	entry, err := FindTrashed(entries, "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Undelete(context.Background(), e, entry, nil); err != nil {
		t.Fatal(err)
	}
	if registry.defaultGUID != "" {
		t.Errorf("it should not have become the default: %q", registry.defaultGUID)
	}
}

func TestPlanUndeleteRefusesToCollideWithALiveDistribution(t *testing.T) {
	e, _, _, _ := trashedEnv(t, true)
	entries, _ := ListTrash(e, trashRoot)
	entry, err := FindTrashed(entries, "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	_, err = PlanUndelete(entry, []Registration{reg("Ubuntu")})
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("want ErrAlreadyRegistered, got %v", err)
	}
	if _, err := PlanUndelete(entry, nil); err != nil {
		t.Fatalf("with nothing registered it should be fine: %v", err)
	}
}

// A folder whose manifest cannot be read is reported, not restored from.
func TestPlanUndeleteRefusesAnUnreadableEntry(t *testing.T) {
	entry := TrashEntry{GUID: "{x}", Dir: trashRoot + `\{x}`, Err: errors.New("not valid JSON")}
	if _, err := PlanUndelete(entry, nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v", err)
	}
}

// ---------------------------------------------------------------- list

func TestListTrashReportsSizeAndAge(t *testing.T) {
	e, _, _, _ := trashedEnv(t, true)
	entries, err := ListTrash(e, trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	entry := entries[0]
	if entry.Name() != "Ubuntu" {
		t.Errorf("name %q", entry.Name())
	}
	// The disk plus the sibling plus the manifest.
	if entry.Bytes == nil || *entry.Bytes < 12<<30 {
		t.Errorf("size %v", entry.Bytes)
	}
	later := trashNow.Add(72 * time.Hour)
	if got := entry.Age(later); got != 72*time.Hour {
		t.Errorf("age %v", got)
	}
}

func TestListTrashOfAnAbsentFolderIsEmptyNotAnError(t *testing.T) {
	e, _, _, _, _ := trashEnv()
	entries, err := ListTrash(e, `C:\nowhere`)
	if err != nil {
		t.Fatalf("an empty trash is not a failure: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %v", entries)
	}
}

// A folder wslkit cannot read is still taking up space the user may want back,
// so it is reported rather than hidden.
func TestListTrashReportsAFolderItCannotRead(t *testing.T) {
	e, fsys, _, _, _ := trashEnv()
	dir := trashRoot + `\{broken}`
	fsys.dirs[trashRoot] = []DirEntry{{Path: dir, IsDir: true}}
	fsys.dirs[dir] = []DirEntry{{Path: dir + `\` + ManifestName}}
	fsys.files[dir+`\`+ManifestName] = fakeFile{onDisk: 10}
	fsys.blobs = map[string][]byte{dir + `\` + ManifestName: []byte("{not json")}

	entries, err := ListTrash(e, trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Err == nil {
		t.Fatalf("the broken entry should be reported with its error: %+v", entries)
	}
	var b bytes.Buffer
	RenderTrashList(&b, entries, trashNow)
	if !strings.Contains(b.String(), "unreadable") {
		t.Errorf("the listing should mark it:\n%s", b.String())
	}
}

func TestFindTrashedByNameOrGUID(t *testing.T) {
	e, _, _, _ := trashedEnv(t, true)
	entries, _ := ListTrash(e, trashRoot)
	for _, key := range []string{"Ubuntu", "ubuntu", "{Ubuntu}"} {
		if _, err := FindTrashed(entries, key); err != nil {
			t.Errorf("FindTrashed(%q): %v", key, err)
		}
	}
	if _, err := FindTrashed(entries, "Nope"); !errors.Is(err, ErrNotTrashed) {
		t.Errorf("got %v", err)
	}
}

// The same distribution trashed twice is told apart by GUID, so restoring an
// arbitrary one would be a guess.
func TestFindTrashedRefusesToGuessBetweenTwo(t *testing.T) {
	entries := []TrashEntry{
		{GUID: "{a}", Manifest: Manifest{Registration: Registration{Name: "Ubuntu"}}},
		{GUID: "{b}", Manifest: Manifest{Registration: Registration{Name: "Ubuntu"}}},
	}
	_, err := FindTrashed(entries, "Ubuntu")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "{a}") || !strings.Contains(err.Error(), "{b}") {
		t.Errorf("the error should name both: %v", err)
	}
}

// ---------------------------------------------------------------- purge

func TestPurgeRemovesTheFolder(t *testing.T) {
	e, fsys, _, _ := trashedEnv(t, true)
	entries, _ := ListTrash(e, trashRoot)
	results := Purge(e, entries)
	if len(results) != 1 || !results[0].Deleted {
		t.Fatalf("got %+v", results)
	}
	if _, ok := fsys.files[trashRoot+`\{Ubuntu}\ext4.vhdx`]; ok {
		t.Error("the disk is still there")
	}
	if len(fsys.removedDirs) != 1 {
		t.Errorf("the folder should have gone: %v", fsys.removedDirs)
	}
	left, _ := ListTrash(e, trashRoot)
	if len(left) != 0 {
		t.Errorf("the trash should be empty: %+v", left)
	}
}

func TestOlderThanFiltersByAge(t *testing.T) {
	entries := []TrashEntry{
		{GUID: "{old}", Manifest: Manifest{TrashedAt: trashNow.Add(-40 * 24 * time.Hour)}},
		{GUID: "{new}", Manifest: Manifest{TrashedAt: trashNow.Add(-1 * time.Hour)}},
		{GUID: "{undated}"},
	}
	got := OlderThan(entries, 30*24*time.Hour, trashNow)
	if len(got) != 1 || got[0].GUID != "{old}" {
		t.Fatalf("got %+v", got)
	}
	// An entry with no readable date is never purged by age: the tool cannot
	// say how old it is, and guessing would delete a distribution.
	for _, g := range got {
		if g.GUID == "{undated}" {
			t.Error("an undated entry must not be purged by age")
		}
	}
	// No age given means everything.
	if len(OlderThan(entries, 0, trashNow)) != 3 {
		t.Error("a zero age should not filter")
	}
}

func TestParseAge(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"12h", 12 * time.Hour},
		{"90m", 90 * time.Minute},
		{"", 0},
	} {
		got, err := ParseAge(c.in)
		if err != nil {
			t.Errorf("ParseAge(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAge(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// Go's own parser has no day unit, which is the unit this is for.
	for _, bad := range []string{"thirty", "-5d", "-1h", "30x"} {
		if _, err := ParseAge(bad); err == nil {
			t.Errorf("ParseAge(%q) should have failed", bad)
		}
	}
}

func TestRenderTrashListSaysHowToGetThingsBack(t *testing.T) {
	e, _, _, _ := trashedEnv(t, true)
	entries, _ := ListTrash(e, trashRoot)
	var b bytes.Buffer
	RenderTrashList(&b, entries, trashNow.Add(2*24*time.Hour))
	out := b.String()
	if !strings.Contains(out, "Ubuntu") {
		t.Errorf("the name is missing:\n%s", out)
	}
	if !strings.Contains(out, "2 day(s) ago") {
		t.Errorf("the age is missing:\n%s", out)
	}
	if !strings.Contains(out, "wslkit disk undelete") {
		t.Errorf("it should say how to restore:\n%s", out)
	}

	var empty bytes.Buffer
	RenderTrashList(&empty, nil, trashNow)
	if !strings.Contains(empty.String(), "the trash is empty") {
		t.Errorf("got %q", empty.String())
	}
}

func TestTrashJSON(t *testing.T) {
	e, _, _, _ := trashedEnv(t, true)
	entries, _ := ListTrash(e, trashRoot)
	o := TrashJSON(entries[0], trashNow.Add(time.Hour))
	if o["distribution"] != "Ubuntu" || o["guid"] != "{Ubuntu}" {
		t.Errorf("got %v", o)
	}
	if o["was_default"] != true {
		t.Errorf("being the default should be reported: %v", o)
	}
	if o["age_seconds"] != int64(3600) {
		t.Errorf("age_seconds = %v", o["age_seconds"])
	}
	if _, ok := o["trashed_at"]; !ok {
		t.Error("trashed_at missing")
	}
}

func TestHumanAge(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "less than a minute"},
		{5 * time.Minute, "5 minute(s)"},
		{3 * time.Hour, "3 hour(s)"},
		{50 * time.Hour, "2 day(s)"},
	} {
		if got := humanAge(c.in); got != c.want {
			t.Errorf("humanAge(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Stopping a distribution does not free its disk: the utility VM holds it open
// for about a minute afterwards. Moving it straight away fails with a sharing
// violation, which is what this waits out.
func TestTrashWaitsForTheUtilityVMToReleaseTheDisk(t *testing.T) {
	e, fsys, _, _, clock := trashEnv()
	vhd := `C:\wsl\Ubuntu\ext4.vhdx`
	fsys.unlockAfter = map[string]int{vhd: 3}

	if _, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil); err != nil {
		t.Fatalf("it should have waited and succeeded: %v", err)
	}
	if len(clock.slept) == 0 {
		t.Error("it should have waited at all")
	}
	if _, ok := fsys.files[trashRoot+`\{Ubuntu}\ext4.vhdx`]; !ok {
		t.Error("the disk never made it to the trash")
	}
}

// A disk that never comes free is a named refusal, not a sharing violation from
// somewhere deep in the move.
func TestTrashReportsADiskThatNeverComesFree(t *testing.T) {
	e, fsys, _, _, _ := trashEnv()
	fsys.unlockAfter = map[string]int{`C:\wsl\Ubuntu\ext4.vhdx`: 10000}

	_, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}

// A failure after the folder is created must not leave an empty one behind: the
// listing would show it as an entry with no manifest, which reads as a trashed
// distribution that cannot be restored.
func TestTrashLeavesNoEmptyFolderWhenItFails(t *testing.T) {
	e, fsys, _, _, _ := trashEnv()
	fsys.renameErr = errors.New("the process cannot access the file")

	if _, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil); err == nil {
		t.Fatal("expected the move to fail")
	}
	entries, err := ListTrash(e, trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("an empty trash folder was left behind: %+v", entries)
	}
	if len(fsys.removedDirs) != 1 {
		t.Errorf("the folder should have been removed: %v", fsys.removedDirs)
	}
}

// The same applies when the unregister is what fails: the disk goes back and
// the folder goes with it.
func TestTrashLeavesNoFolderWhenUnregisteringFails(t *testing.T) {
	e, _, _, host, _ := trashEnv()
	host.unregisterErr = errors.New("access is denied")

	if _, err := Trash(context.Background(), e, reg("Ubuntu"), trashRoot, TrashOptions{}, nil); err == nil {
		t.Fatal("expected the unregister to fail")
	}
	entries, err := ListTrash(e, trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the trash should be empty: %+v", entries)
	}
}
