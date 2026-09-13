package disk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	srcDir  = `C:\wsl\Ubuntu`
	srcPath = srcDir + `\ext4.vhdx`
)

func moveEnv(destDir string, destFS string, free uint64) (Env, *fakeFS, *fakeRegistry, *fakeHost) {
	fsys := &fakeFS{
		files: map[string]fakeFile{srcPath: {size: 100 << 30, onDisk: 12 << 30}},
		volumes: map[string]VolumeInfo{
			"C:": {Root: `C:\`, FileSystem: "NTFS", FreeBytes: 500 << 30},
			"D:": {Root: `D:\`, FileSystem: destFS, FreeBytes: free},
		},
	}
	registry := &fakeRegistry{
		values: map[string]string{
			"{Ubuntu}\x00BasePath":    srcDir,
			"{Ubuntu}\x00VhdFileName": "ext4.vhdx",
		},
	}
	host := &fakeHost{}
	return Env{Registry: registry, FS: fsys, Disks: &fakeDisks{}, Host: host, Clock: &fakeClock{}}, fsys, registry, host
}

func TestNewMoveTargetKeepsTheFileName(t *testing.T) {
	tgt := NewMoveTarget(srcPath, `D:\wsl`)
	if tgt.Path != `D:\wsl\ext4.vhdx` {
		t.Errorf("got %q", tgt.Path)
	}
}

// Within a volume a rename is instant and cannot half-succeed. Across volumes
// the bytes have to move.
func TestMoveRenamesWithinAVolumeAndCopiesAcross(t *testing.T) {
	e, fsys, _, _ := moveEnv(`C:\dest`, "NTFS", 500<<30)
	res, err := Move(context.Background(), e, reg("Ubuntu"), NewMoveTarget(srcPath, `C:\dest`), MoveOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Renamed || !res.SameVolume {
		t.Errorf("expected a rename: %+v", res)
	}
	if len(fsys.renames) != 1 || len(fsys.copies) != 0 {
		t.Errorf("renames=%v copies=%v", fsys.renames, fsys.copies)
	}

	e2, fsys2, _, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
	res2, err := Move(context.Background(), e2, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Renamed || res2.SameVolume {
		t.Errorf("expected a copy: %+v", res2)
	}
	if len(fsys2.copies) != 1 {
		t.Errorf("copies=%v", fsys2.copies)
	}
	// The original goes only after the copy is proved to boot.
	if len(fsys2.removed) != 1 || fsys2.removed[0] != srcPath {
		t.Errorf("removed=%v", fsys2.removed)
	}
}

// --keep-source turns a same-volume move into a copy, because the whole point
// is that the original stays where it is.
func TestKeepSourceForcesACopyAndLeavesTheOriginal(t *testing.T) {
	e, fsys, _, _ := moveEnv(`C:\dest`, "NTFS", 500<<30)
	res, err := Move(context.Background(), e, reg("Ubuntu"), NewMoveTarget(srcPath, `C:\dest`), MoveOptions{KeepSource: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed {
		t.Error("a kept source cannot be a rename")
	}
	if len(fsys.removed) != 0 {
		t.Errorf("the original should still be there: %v", fsys.removed)
	}
	if _, ok := fsys.files[srcPath]; !ok {
		t.Error("the source file is gone")
	}
}

// The original is deleted last. If the distribution will not start from the new
// location, everything goes back and the original is untouched.
func TestMoveRollsBackWhenTheDistroWillNotStart(t *testing.T) {
	e, fsys, registry, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
	e.Host = &scriptedHost{responses: []CommandResult{{ExitCode: 1, Stderr: "no such file"}}}

	_, err := Move(context.Background(), e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, nil)
	if !errors.Is(err, ErrSmokeTest) {
		t.Fatalf("want ErrSmokeTest, got %v", err)
	}
	if _, ok := fsys.files[srcPath]; !ok {
		t.Error("the original must still be there")
	}
	if _, ok := fsys.files[`D:\wsl\ext4.vhdx`]; ok {
		t.Error("the copy should have been removed")
	}
	if got := registry.values["{Ubuntu}\x00BasePath"]; got != srcDir {
		t.Errorf("the registration was not restored: %q", got)
	}
}

// A rename that has to be undone is moved back, never deleted: the file at the
// target is the original.
func TestMoveUndoesARenameByMovingItBack(t *testing.T) {
	e, fsys, _, _ := moveEnv(`C:\dest`, "NTFS", 500<<30)
	e.Host = &scriptedHost{responses: []CommandResult{{ExitCode: 1}}}

	if _, err := Move(context.Background(), e, reg("Ubuntu"), NewMoveTarget(srcPath, `C:\dest`), MoveOptions{}, nil); err == nil {
		t.Fatal("expected the boot check to fail")
	}
	if _, ok := fsys.files[srcPath]; !ok {
		t.Fatal("the disk was not moved back")
	}
	if len(fsys.removed) != 0 {
		t.Errorf("nothing should have been deleted: %v", fsys.removed)
	}
	if len(fsys.renames) != 2 {
		t.Errorf("expected a rename and its reverse: %v", fsys.renames)
	}
}

// The move worked; the leftover did not go away. That is worth reporting and
// not worth undoing a good move for.
func TestMoveReportsButSurvivesAFailureToDeleteTheOriginal(t *testing.T) {
	e, fsys, registry, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
	// Removing the source fails because the fake only knows the copy.
	delete(fsys.files, srcPath)
	fsys.files[srcPath] = fakeFile{size: 100 << 30, onDisk: 12 << 30}
	e.FS = &removeFailsFS{fakeFS: fsys}

	_, err := Move(context.Background(), e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, nil)
	if err == nil {
		t.Fatal("the leftover should have been reported")
	}
	if !strings.Contains(err.Error(), "could not be deleted") {
		t.Errorf("got %v", err)
	}
	// The registration must still point at the new location: the move did
	// work.
	if got := registry.values["{Ubuntu}\x00BasePath"]; got != `D:\wsl` {
		t.Errorf("the move was undone: %q", got)
	}
}

type removeFailsFS struct {
	*fakeFS
}

func (f *removeFailsFS) Remove(path string) error { return errors.New("access is denied") }

func TestPlanMoveRefusals(t *testing.T) {
	t.Run("target exists", func(t *testing.T) {
		e, fsys, _, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
		fsys.files[`D:\wsl\ext4.vhdx`] = fakeFile{}
		_, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, false, true)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("not enough room", func(t *testing.T) {
		// The disk costs 12 GiB, so 12 GiB exactly is not enough: the
		// margin exists so a move does not fill the volume completely.
		e, _, _, _ := moveEnv(`D:\wsl`, "NTFS", 12<<30)
		_, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, false, true)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "free") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("filesystem cannot hold it", func(t *testing.T) {
		e, _, _, _ := moveEnv(`D:\wsl`, "exFAT", 500<<30)
		_, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, false, true)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "4 GiB") {
			t.Fatalf("the refusal should say why: %v", err)
		}
	})

	t.Run("running", func(t *testing.T) {
		e, _, _, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
		if _, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, true, true); !errors.Is(err, ErrRunning) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("running unknown", func(t *testing.T) {
		e, _, _, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
		if _, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, false, false); !errors.Is(err, ErrRefused) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("already there", func(t *testing.T) {
		e, _, _, _ := moveEnv(srcDir, "NTFS", 500<<30)
		_, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, srcDir), MoveOptions{}, false, true)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "already at") {
			t.Fatalf("got %v", err)
		}
	})
}

// A same-volume rename does not need the destination checks: no bytes are
// written, so the free space on the volume does not change.
func TestPlanMoveSkipsTheSpaceCheckForARename(t *testing.T) {
	e, fsys, _, _ := moveEnv(`C:\dest`, "NTFS", 0)
	fsys.volumes["C:"] = VolumeInfo{Root: `C:\`, FileSystem: "NTFS", FreeBytes: 0}
	if _, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `C:\dest`), MoveOptions{}, false, true); err != nil {
		t.Fatalf("a rename needs no room: %v", err)
	}
}

// The delete is the point of no return, so it must be last and nothing
// undoable may follow it.
func TestPlanMoveOrdersTheIrreversibleStepLast(t *testing.T) {
	e, _, _, _ := moveEnv(`D:\wsl`, "NTFS", 500<<30)
	p, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Valid(); err != nil {
		t.Fatalf("plan should be valid: %v", err)
	}
	last := p.Steps[len(p.Steps)-1]
	if !last.Irreversible || !strings.HasPrefix(last.Description, "delete ") {
		t.Fatalf("the delete should be last and irreversible: %+v", p.Steps)
	}
	// And a kept source has no irreversible step at all.
	kept, err := PlanMove(e, reg("Ubuntu"), NewMoveTarget(srcPath, `D:\wsl`), MoveOptions{KeepSource: true}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range kept.Steps {
		if s.Irreversible {
			t.Errorf("nothing is irreversible when the source is kept: %+v", s)
		}
	}
}

func TestMoveJSON(t *testing.T) {
	o := MoveJSON(MoveResult{Distro: "Ubuntu", VhdPath: `D:\wsl\ext4.vhdx`, BasePath: `D:\wsl`, SizeOnDisk: u64(12 << 30)})
	if o["moved"] != true || o["renamed"] != false || o["kept_source"] != false {
		t.Errorf("unexpected: %v", o)
	}
	if o["size_on_disk"] != uint64(12<<30) {
		t.Errorf("size: %v", o["size_on_disk"])
	}
	// base_path is what was actually written, prefix and all.
	if o["base_path"] != `D:\wsl` {
		t.Errorf("base_path: %v", o["base_path"])
	}
}
