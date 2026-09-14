package disk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// rebuildEnv is a machine with one distribution whose disk is the given size.
func rebuildEnv(t *testing.T, size uint64) (Env, *fakeFS, *fakeHost, *fakeRegistry) {
	t.Helper()
	const vhd = `C:\wsl\Ubuntu\ext4.vhdx`
	fsys := &fakeFS{
		files:   map[string]fakeFile{vhd: {size: size, onDisk: size}},
		volumes: map[string]VolumeInfo{"C:": {Root: `C:`, FileSystem: "NTFS", FreeBytes: size * 10}},
	}
	host := &fakeHost{fs: fsys}
	registry := &fakeRegistry{
		list: []Registration{{
			GUID: "{Ubuntu}", Name: "Ubuntu", Version: 2,
			BasePath: `C:\wsl\Ubuntu`, VhdFileName: "ext4.vhdx",
			DefaultUID: 1000, Flags: 7,
		}},
		values: map[string]string{},
	}
	e := Env{FS: fsys, Registry: registry, Disks: &fakeDisks{}, Host: host, Clock: &fakeClock{now: time.Unix(1757800000, 0)}}
	return e, fsys, host, registry
}

func rebuildReg(e Env) Registration {
	list, _, _ := e.Registry.Distros()
	return list[0]
}

func TestPlanRebuildOrdersTheIrreversibleStepLast(t *testing.T) {
	e, _, _, _ := rebuildEnv(t, 8<<30)
	p, err := PlanRebuild(e, rebuildReg(e), RebuildOptions{}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	// The structural rule: nothing that registers a rollback may be
	// scheduled after the point of no return, because there would be no
	// rollback to run.
	if err := p.Valid(); err != nil {
		t.Fatalf("the plan breaks its own rule: %v", err)
	}
	joined := strings.Join(stepText(p), "\n")
	for _, want := range []string{"export Ubuntu", "unregister Ubuntu", "import Ubuntu again", "put back", "start Ubuntu"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the plan should mention %q:\n%s", want, joined)
		}
	}
	// The export has to come before the unregister. That order is the whole
	// safety argument.
	if strings.Index(joined, "export Ubuntu") > strings.Index(joined, "unregister Ubuntu") {
		t.Error("the export must happen before anything is deleted")
	}
}

func stepText(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.Description)
	}
	return out
}

// Refusals are checked before anything is stopped, so they cost nothing.
func TestPlanRebuildRefusals(t *testing.T) {
	t.Run("no room for the archive", func(t *testing.T) {
		e, fsys, _, _ := rebuildEnv(t, 8<<30)
		fsys.volumes["C:"] = VolumeInfo{Root: `C:`, FreeBytes: 1 << 20}
		_, err := PlanRebuild(e, rebuildReg(e), RebuildOptions{}, false, true)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "free") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("running state unknown", func(t *testing.T) {
		e, _, _, _ := rebuildEnv(t, 1<<30)
		_, err := PlanRebuild(e, rebuildReg(e), RebuildOptions{}, false, false)
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("WSL 1", func(t *testing.T) {
		e, _, _, _ := rebuildEnv(t, 1<<30)
		r := rebuildReg(e)
		r.Version = 1
		if _, err := PlanRebuild(e, r, RebuildOptions{}, false, true); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("disk missing", func(t *testing.T) {
		e, fsys, _, _ := rebuildEnv(t, 1<<30)
		delete(fsys.files, `C:\wsl\Ubuntu\ext4.vhdx`)
		if _, err := PlanRebuild(e, rebuildReg(e), RebuildOptions{}, false, true); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
}

// The happy path, and the thing that makes rebuild worth having over doing it
// by hand: the settings an import drops are put back.
func TestRebuildRestoresTheRegistration(t *testing.T) {
	e, fsys, host, registry := rebuildEnv(t, 4<<30)
	r := rebuildReg(e)
	registry.defaultGUID = "{Ubuntu}"

	host.onImportTar = func(name, dir, tarPath string) {
		// What wsl.exe does: a new registration with a new GUID, and none
		// of the old settings.
		registry.list = []Registration{{
			GUID: "{fresh}", Name: name, Version: 2,
			BasePath: dir, VhdFileName: "ext4.vhdx",
			DefaultUID: 0, Flags: 0,
		}}
		fsys.files[dir+`\ext4.vhdx`] = fakeFile{size: 2 << 30, onDisk: 2 << 30}
	}

	res, err := Rebuild(context.Background(), e, r, RebuildOptions{RunningBefore: map[string]bool{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.dwords["{fresh}\x00DefaultUid"]; got != 1000 {
		t.Errorf("DefaultUid = %d, want the old 1000", got)
	}
	if got := registry.dwords["{fresh}\x00Flags"]; got != 7 {
		t.Errorf("Flags = %d, want the old 7", got)
	}
	if registry.defaultGUID != "{fresh}" {
		t.Errorf("the default distribution marker was not moved: %q", registry.defaultGUID)
	}
	if rec := res.Reclaimed(); rec == nil || *rec != 2<<30 {
		t.Errorf("reclaimed = %v, want 2 GiB", rec)
	}
	// The archive is a working file, not a deliverable, and is gone unless
	// asked for.
	if res.ArchiveKept {
		t.Error("the archive should have been deleted")
	}
	if _, ok := fsys.files[res.Archive]; ok {
		t.Error("the archive file is still on disk")
	}
}

func TestRebuildKeepsTheArchiveWhenAsked(t *testing.T) {
	e, fsys, host, registry := rebuildEnv(t, 1<<30)
	host.onImportTar = func(name, dir, tarPath string) {
		registry.list = []Registration{{GUID: "{fresh}", Name: name, Version: 2, BasePath: dir, VhdFileName: "ext4.vhdx"}}
		fsys.files[dir+`\ext4.vhdx`] = fakeFile{size: 1 << 29, onDisk: 1 << 29}
	}
	res, err := Rebuild(context.Background(), e, rebuildReg(e), RebuildOptions{KeepArchive: true, RunningBefore: map[string]bool{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ArchiveKept {
		t.Fatal("the archive should have been kept")
	}
	if _, ok := fsys.files[res.Archive]; !ok {
		t.Error("the archive is not there")
	}
}

// A failed export must leave the distribution exactly as it was: this is the
// half of the operation where nothing has been destroyed yet.
func TestRebuildFailedExportChangesNothing(t *testing.T) {
	e, _, host, _ := rebuildEnv(t, 1<<30)
	host.exportErr = errors.New("wsl.exe: access denied")
	_, err := Rebuild(context.Background(), e, rebuildReg(e), RebuildOptions{RunningBefore: map[string]bool{}}, nil)
	if err == nil {
		t.Fatal("expected the export failure to be reported")
	}
	if !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("the error should say the distribution is untouched: %v", err)
	}
	for _, call := range host.calls {
		if strings.HasPrefix(call, "unregister") {
			t.Fatal("nothing may be unregistered after a failed export")
		}
	}
}

// The dangerous case: the old disk is gone and the import fails. The archive
// must survive, and the error must say how to use it.
func TestRebuildFailedImportKeepsTheArchiveAndSaysHow(t *testing.T) {
	e, fsys, host, _ := rebuildEnv(t, 1<<30)
	host.importErr = errors.New("wsl.exe: invalid archive")

	res, err := Rebuild(context.Background(), e, rebuildReg(e), RebuildOptions{RunningBefore: map[string]bool{}}, nil)
	if err == nil {
		t.Fatal("expected the import failure to be reported")
	}
	if !res.ArchiveKept {
		t.Fatal("the archive is the only copy left and must be kept")
	}
	if _, ok := fsys.files[res.Archive]; !ok {
		t.Error("the archive was deleted after a failed import")
	}
	if !strings.Contains(err.Error(), "wsl --import") {
		t.Errorf("the error should give the command that recovers it: %v", err)
	}
}

func TestRebuildJSONCarriesTheNumbers(t *testing.T) {
	res := RebuildResult{
		Distro: "Ubuntu", Before: u64(4 << 30), After: u64(1 << 30),
		Archive: `C:\wsl\x.tar`, VhdPath: `C:\wsl\Ubuntu\ext4.vhdx`,
		Restored: []string{"default user", "flags"},
	}
	m := RebuildJSON(res)
	if m["reclaimed"] != uint64(3<<30) {
		t.Errorf("reclaimed = %v", m["reclaimed"])
	}
	if m["archive_kept"] != false {
		t.Errorf("archive_kept = %v", m["archive_kept"])
	}
}
