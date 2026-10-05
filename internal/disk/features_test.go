package disk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------- rename (#86)

func renameEnv() (Env, *fakeRegistry, *fakeHost) {
	registry := &fakeRegistry{}
	host := &fakeHost{}
	return Env{Registry: registry, FS: &fakeFS{}, Disks: &fakeDisks{}, Host: host, Clock: &fakeClock{}}, registry, host
}

func TestPlanRenameRefusals(t *testing.T) {
	e, _, _ := renameEnv()
	list := []Registration{reg("Ubuntu"), reg("Debian")}
	for _, tc := range []struct{ name, to, want string }{
		{"empty", " ", "empty"},
		{"space", "my distro", "not a name WSL accepts"},
		{"colon", "a:b", "not a name WSL accepts"},
		{"non-ascii", "Übuntu", "not a name WSL accepts"},
		{"same", "Ubuntu", "already called that"},
		// wsl.exe matches names case-insensitively.
		{"taken", "debian", "already exists"},
	} {
		if _, err := PlanRename(e, list[0], tc.to, list, false, true); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if _, err := PlanRename(e, list[0], "Work", list, true, true); !errors.Is(err, ErrRunning) {
		t.Errorf("running: %v", err)
	}
	if _, err := PlanRename(e, list[0], "Work", list, false, false); !errors.Is(err, ErrRefused) {
		t.Errorf("running unknown: %v", err)
	}
	// A change of case alone is the same distribution, and allowed.
	if _, err := PlanRename(e, list[0], "ubuntu", list, false, true); err != nil {
		t.Errorf("case-only rename: %v", err)
	}
	p, err := PlanRename(e, list[0], "Work.2-dev_x", list, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Warnings[0].Message, `\\wsl.localhost\Ubuntu`) {
		t.Errorf("the plan should say what breaks: %+v", p.Warnings)
	}
}

func TestRenameWritesTheNameAndProvesIt(t *testing.T) {
	e, registry, host := renameEnv()
	res, err := Rename(context.Background(), e, reg("Ubuntu"), "Work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewName != "Work" || len(registry.writes) != 1 || registry.writes[0] != "{Ubuntu}/DistributionName=Work" {
		t.Errorf("writes = %v", registry.writes)
	}
	// Started under the new name, which is the proof it resolves.
	if len(host.calls) != 1 || !strings.HasPrefix(host.calls[0], "run Work ") {
		t.Errorf("calls = %v", host.calls)
	}
}

// A distribution that does not start under its new name gets its old one back.
func TestRenameThatDoesNotStartIsPutBack(t *testing.T) {
	e, registry, host := renameEnv()
	host.runErr = errors.New("boom")
	_, err := Rename(context.Background(), e, reg("Ubuntu"), "Work", nil)
	if !errors.Is(err, ErrSmokeTest) {
		t.Fatalf("err = %v", err)
	}
	if got := registry.values[registry.key("{Ubuntu}", "DistributionName")]; got != "Ubuntu" {
		t.Errorf("the old name was not put back: %q (writes %v)", got, registry.writes)
	}
}

// ---------------------------------------------------------------- flags (#92)

func on(b bool) *bool { return &b }

// The undocumented bit WSL sets must survive any change to the others.
func TestApplyFlagsKeepsTheBitsItWasNotAskedAbout(t *testing.T) {
	// 15 is interop, append-path, drive-mounting and the undocumented 0x8.
	if got := ApplyFlags(15, FlagChange{Interop: on(false)}); got != 14 {
		t.Errorf("interop off from 15: %d", got)
	}
	if got := ApplyFlags(14, FlagChange{Interop: on(true), Automount: on(false)}); got != 11 {
		t.Errorf("interop on, automount off from 14: %d", got)
	}
	if got := ApplyFlags(8, FlagChange{}); got != 8 {
		t.Errorf("no change: %d", got)
	}
}

func TestParseOnOff(t *testing.T) {
	for in, want := range map[string]bool{"on": true, "OFF": false, "true": true, "false": false} {
		if got, err := ParseOnOff(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "1", "yes", "enabled"} {
		if _, err := ParseOnOff(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestPlanFlags(t *testing.T) {
	r := reg("Ubuntu", func(r *Registration) { r.Flags = 15 })
	if _, _, err := PlanFlags(r, FlagChange{}, false); !errors.Is(err, ErrRefused) {
		t.Errorf("nothing named: %v", err)
	}
	if _, _, err := PlanFlags(r, FlagChange{Interop: on(true)}, false); !errors.Is(err, ErrRefused) {
		t.Errorf("no-op: %v", err)
	}
	p, next, err := PlanFlags(r, FlagChange{AppendPath: on(false)}, true)
	if err != nil || next != 13 {
		t.Fatalf("next = %d, err = %v", next, err)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Remedy, "wsl --terminate Ubuntu") {
		t.Errorf("a running distribution reads these at start: %+v", p.Warnings)
	}
}

func TestLookupUserPassesTheNameAsAnArgument(t *testing.T) {
	host := &fakeHost{byArgs: map[string]CommandResult{
		`/bin/sh -c id -u "$1" && id -nu "$1" sh zoe`: {Stdout: "1000\nzoe\n"},
	}}
	e := Env{Host: host}
	uid, name, err := LookupUser(context.Background(), e, "Ubuntu", "zoe")
	if err != nil || uid != 1000 || name != "zoe" {
		t.Fatalf("uid %d name %q err %v", uid, name, err)
	}
	host.byArgs[`/bin/sh -c id -u "$1" && id -nu "$1" sh nobody2`] = CommandResult{ExitCode: 1}
	if _, _, err := LookupUser(context.Background(), e, "Ubuntu", "nobody2"); !errors.Is(err, ErrRefused) {
		t.Errorf("an unknown user: %v", err)
	}
}

// ---------------------------------------------------------------- snapshot (#85)

const snapRoot = `C:\Users\u\AppData\Local\wslkit\snapshots`

func snapEnv() (Env, *fakeFS, *fakeHost, *fakeClock) {
	fsys := &fakeFS{
		files:   map[string]fakeFile{`C:\wsl\Ubuntu\ext4.vhdx`: {size: 100 << 30, onDisk: 3 << 30}},
		volumes: map[string]VolumeInfo{"C:": {Root: `C:\`, FileSystem: "NTFS", FreeBytes: 50 << 30}},
	}
	host := &fakeHost{}
	clock := &fakeClock{now: time.Date(2026, 10, 5, 14, 30, 12, 0, time.UTC)}
	return Env{Registry: &fakeRegistry{}, FS: fsys, Disks: &fakeDisks{}, Host: host, Clock: clock}, fsys, host, clock
}

func TestPlanSnapshotChecksRoom(t *testing.T) {
	e, fsys, _, _ := snapEnv()
	if _, err := PlanSnapshot(e, reg("Ubuntu"), snapRoot, SnapshotOptions{}, false, true); err != nil {
		t.Fatalf("enough room: %v", err)
	}
	// Measured against what the file costs, 3 GiB, not its virtual size.
	fsys.volumes["C:"] = VolumeInfo{Root: `C:\`, FileSystem: "NTFS", FreeBytes: 2 << 30}
	if _, err := PlanSnapshot(e, reg("Ubuntu"), snapRoot, SnapshotOptions{}, false, true); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "needs") {
		t.Errorf("no room: %v", err)
	}
}

func TestSnapshotThenRestoreKeepsWhatItReplaced(t *testing.T) {
	e, fsys, host, clock := snapEnv()
	r := reg("Ubuntu")
	entry, err := TakeSnapshot(context.Background(), e, r, snapRoot, SnapshotOptions{Label: "before upgrade", Runtime: "3.0.1.0"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Manifest.ID != "20261005-143012" || entry.Manifest.GUID != "{Ubuntu}" || entry.Manifest.SizeOnDisk != 3<<30 {
		t.Fatalf("manifest = %+v", entry.Manifest)
	}
	if want := snapRoot + `\{Ubuntu}\20261005-143012\ext4.vhdx`; !fsys.Exists(want) {
		t.Fatalf("the copy is not at %s: %v", want, fsys.copies)
	}

	// It lists, and is found by id, label and "latest".
	all := ListSnapshots(e, snapRoot)
	for _, id := range []string{"20261005-143012", "before upgrade", "latest"} {
		if s, err := FindSnapshot(all, r, id); err != nil || s.Manifest.ID != "20261005-143012" {
			t.Errorf("find %q: %v", id, err)
		}
	}
	// Kept by GUID: another distribution of the same name does not get it.
	if _, err := FindSnapshot(all, reg("Ubuntu", func(r *Registration) { r.GUID = "{other}" }), "latest"); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("another GUID: %v", err)
	}

	clock.now = clock.now.Add(time.Hour)
	s, _ := FindSnapshot(all, r, "latest")
	if _, err := PlanRestore(e, r, s, false, true); err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	res, err := Restore(context.Background(), e, r, s, snapRoot, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Before == "" || res.BeforeErr != nil {
		t.Fatalf("the replaced disk was not kept: %+v", res)
	}
	if !fsys.Exists(`C:\wsl\Ubuntu\ext4.vhdx`) || fsys.Exists(`C:\wsl\Ubuntu\ext4.vhdx`+beforeRestoreSuffix) {
		t.Errorf("live disk / aside file wrong: %v", fsys.files)
	}
	if !fsys.Exists(snapRoot + `\{Ubuntu}\` + res.Before + `\ext4.vhdx`) {
		t.Errorf("the replaced disk is not a snapshot: %v", fsys.renames)
	}
	if !strings.Contains(strings.Join(host.calls, ";"), "run Ubuntu") {
		t.Errorf("the restored disk was not started: %v", host.calls)
	}
}

// A restore that does not boot puts the disk it replaced back, and leaves no
// half-restored file behind.
func TestRestoreThatDoesNotBootIsPutBack(t *testing.T) {
	e, fsys, host, _ := snapEnv()
	r := reg("Ubuntu")
	entry, err := TakeSnapshot(context.Background(), e, r, snapRoot, SnapshotOptions{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	live := `C:\wsl\Ubuntu\ext4.vhdx`
	fsys.files[live] = fakeFile{size: 100 << 30, onDisk: 7 << 30} // the "current" disk, recognisable
	host.runErr = errors.New("init failed")
	if _, err := Restore(context.Background(), e, r, entry, snapRoot, false, nil); !errors.Is(err, ErrSmokeTest) {
		t.Fatalf("err = %v", err)
	}
	if got := fsys.files[live]; got.onDisk != 7<<30 {
		t.Errorf("the original disk is not back in place: %+v", got)
	}
	if fsys.Exists(live + beforeRestoreSuffix) {
		t.Error("the aside file was left behind")
	}
}

func TestPlanRestoreRefusesAnotherDistributionsSnapshot(t *testing.T) {
	e, _, _, _ := snapEnv()
	s := SnapshotEntry{Dir: snapRoot + `\{Debian}\x`, Manifest: SnapshotManifest{ID: "x", GUID: "{Debian}", Distribution: "Debian", VhdFile: "ext4.vhdx"}}
	if _, err := PlanRestore(e, reg("Ubuntu"), s, false, true); !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v", err)
	}
}

// ---------------------------------------------------------------- automount (#88)

func TestMountArgs(t *testing.T) {
	if got := strings.Join(AutomountEntry{Path: `D:\data.vhdx`, Name: "data", Type: "xfs", Options: "noatime", Partition: 2}.MountArgs(), " "); got !=
		`--mount D:\data.vhdx --vhd --name data --type xfs --options noatime --partition 2` {
		t.Errorf("got %q", got)
	}
	if got := strings.Join(AutomountEntry{Path: `D:\raw.vhdx`, Bare: true}.MountArgs(), " "); got != `--mount D:\raw.vhdx --vhd --bare` {
		t.Errorf("bare: %q", got)
	}
}

func TestValidateAutomount(t *testing.T) {
	list := []Registration{reg("Ubuntu")}
	table := []AutomountEntry{{Path: `D:\data.vhdx`}}
	for _, tc := range []struct {
		entry AutomountEntry
		want  string
	}{
		{AutomountEntry{Path: `data.vhdx`}, "not an absolute"},
		{AutomountEntry{Path: `D:\disk.img`}, "not a .vhdx"},
		{AutomountEntry{Path: `D:\x.vhdx`, Bare: true, Name: "x"}, "--bare"},
		{AutomountEntry{Path: `D:\x.vhdx`, Name: "a b"}, "mount name"},
		// Never a registered distribution's own disk.
		{AutomountEntry{Path: `c:\WSL\Ubuntu\ext4.vhdx`}, "disk of the distribution Ubuntu"},
		{AutomountEntry{Path: `d:\DATA.vhdx`}, "already in the table"},
	} {
		if err := ValidateAutomount(tc.entry, table, list); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.entry, err, tc.want)
		}
	}
	if err := ValidateAutomount(AutomountEntry{Path: `D:\other.vhdx`, Name: "other"}, table, list); err != nil {
		t.Errorf("valid: %v", err)
	}
}

type fakeMounter struct {
	calls []string
	code  int
}

func (m *fakeMounter) Mount(ctx context.Context, args []string) (CommandResult, error) {
	m.calls = append(m.calls, strings.Join(args, " "))
	return CommandResult{ExitCode: m.code, Stderr: "boom"}, nil
}

// Idempotent: a disk already held is left alone rather than attached twice,
// and one failure does not stop the rest.
func TestApplyAutomount(t *testing.T) {
	fsys := &fakeFS{files: map[string]fakeFile{
		`D:\held.vhdx`: {locked: true},
		`D:\free.vhdx`: {},
	}}
	m := &fakeMounter{}
	res := ApplyAutomount(context.Background(), fsys, m, []AutomountEntry{
		{Path: `D:\held.vhdx`}, {Path: `D:\gone.vhdx`}, {Path: `D:\free.vhdx`, Name: "free"},
	})
	got := []AutomountOutcome{res[0].Outcome, res[1].Outcome, res[2].Outcome}
	want := []AutomountOutcome{AutomountAlreadyHeld, AutomountMissing, AutomountAttached}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: %s, want %s", i, got[i], want[i])
		}
	}
	if len(m.calls) != 1 || m.calls[0] != `--mount D:\free.vhdx --vhd --name free` {
		t.Errorf("mount calls = %v", m.calls)
	}
	m.code = 1
	if r := ApplyAutomount(context.Background(), fsys, m, []AutomountEntry{{Path: `D:\free.vhdx`}}); r[0].Outcome != AutomountFailed {
		t.Errorf("a failed mount: %+v", r[0])
	}
}

// The table lives in the settings file as [[automount]] blocks and has to
// survive being written and read back, Windows paths and all.
func TestAutomountTableRoundTrips(t *testing.T) {
	c := DefaultConfig()
	c.Automount = []AutomountEntry{
		{Path: `D:\disks\data.vhdx`, Name: "data", Options: "noatime"},
		{Path: `E:\raw "quoted".vhdx`, Bare: true},
		{Path: `F:\p.vhdx`, Type: "xfs", Partition: 3},
	}
	back, err := ParseConfig(RenderConfig(c))
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Automount) != 3 {
		t.Fatalf("got %+v", back.Automount)
	}
	for i := range c.Automount {
		if back.Automount[i] != c.Automount[i] {
			t.Errorf("entry %d: %+v, want %+v", i, back.Automount[i], c.Automount[i])
		}
	}
	// The other settings are untouched by the new blocks.
	if back.CompactTrim != c.CompactTrim || back.UnlockTimeoutSeconds != c.UnlockTimeoutSeconds {
		t.Errorf("settings changed: %+v", back)
	}
	if _, err := ParseConfig("[[automount]]\npath = \"D:\\\\x.vhdx\"\npartition = -1\n"); !errors.Is(err, ErrRefused) {
		t.Errorf("a negative partition: %v", err)
	}
}
