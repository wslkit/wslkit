package disk

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func orphanEnv(dirs map[string][]DirEntry, files map[string]fakeFile) Env {
	fsys := &fakeFS{
		dirs:  dirs,
		files: files,
		env:   map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`},
	}
	return Env{FS: fsys, Disks: &fakeDisks{}, Host: &fakeHost{}, Clock: &fakeClock{}}
}

func TestScanOrphansSkipsWhatIsClaimed(t *testing.T) {
	wslDir := `C:\Users\u\AppData\Local\wsl`
	claimed := wslDir + `\{a}\ext4.vhdx`
	orphan := wslDir + `\{b}\ext4.vhdx`
	e := orphanEnv(map[string][]DirEntry{
		wslDir:          {{Path: wslDir + `\{a}`, IsDir: true}, {Path: wslDir + `\{b}`, IsDir: true}},
		wslDir + `\{a}`: {{Path: claimed}},
		wslDir + `\{b}`: {{Path: orphan}},
	}, map[string]fakeFile{claimed: {onDisk: 100}, orphan: {onDisk: 200}})

	regs := []Registration{reg("Ubuntu", func(r *Registration) { r.BasePath = wslDir + `\{a}` })}
	got, warnings, err := ScanOrphans(e, regs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(got) != 1 || got[0].Path != orphan {
		t.Fatalf("got %+v, want only %s", got, orphan)
	}
	if got[0].SizeOnDisk == nil || *got[0].SizeOnDisk != 200 {
		t.Errorf("size: %v", got[0].SizeOnDisk)
	}
}

// A registration stored with the extended-length prefix claims the same file as
// one stored without it. Comparing the raw strings would report the user's own
// distribution as an orphan.
func TestScanOrphansComparesCanonically(t *testing.T) {
	wslDir := `C:\Users\u\AppData\Local\wsl`
	disk := wslDir + `\{a}\ext4.vhdx`
	e := orphanEnv(map[string][]DirEntry{
		wslDir:          {{Path: wslDir + `\{a}`, IsDir: true}},
		wslDir + `\{a}`: {{Path: disk}},
	}, map[string]fakeFile{disk: {onDisk: 100}})

	regs := []Registration{reg("Ubuntu", func(r *Registration) { r.BasePath = `\\?\` + wslDir + `\{a}` })}
	got, _, err := ScanOrphans(e, regs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the claimed disk was reported as an orphan: %+v", got)
	}
}

// The filesystem matches *.vhdx against the 8.3 short name too, so a search for
// *.vhdx returns disk.vhdx.bak. Deleting that because it looked like an orphan
// would be destroying a backup.
func TestScanOrphansRechecksTheExtension(t *testing.T) {
	dir := `C:\Users\u\AppData\Local\Docker\wsl`
	sub := dir + `\disk`
	e := orphanEnv(map[string][]DirEntry{
		dir: {{Path: sub, IsDir: true}},
		sub: {
			{Path: sub + `\data.vhdx`},
			{Path: sub + `\data.vhdx.bak`},
			{Path: sub + `\nested`, IsDir: true},
		},
	}, map[string]fakeFile{sub + `\data.vhdx`: {onDisk: 5}, sub + `\data.vhdx.bak`: {onDisk: 5}})

	got, _, err := ScanOrphans(e, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0].Path, "data.vhdx") {
		t.Fatalf("got %+v", got)
	}
}

// A directory reachable through two patterns must be reported once.
func TestScanOrphansDoesNotReportADiskTwice(t *testing.T) {
	dir := `C:\Users\u\AppData\Local\wsl`
	sub := dir + `\{a}`
	e := orphanEnv(map[string][]DirEntry{
		dir: {{Path: sub, IsDir: true}},
		sub: {{Path: sub + `\ext4.vhdx`}},
	}, map[string]fakeFile{sub + `\ext4.vhdx`: {onDisk: 1}})

	// The same directory, passed again as an explicit scan root.
	got, _, err := ScanOrphans(e, nil, []string{sub})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one report, got %+v", got)
	}
}

// One unreadable directory must not stop the scan reporting everything else.
func TestScanOrphansWarnsAboutWhatItCouldNotRead(t *testing.T) {
	e := orphanEnv(nil, nil)
	e.FS.(*fakeFS).listErr = map[string]error{
		`C:\Users\u\AppData\Local\wsl`: errors.New("access is denied"),
	}
	_, warnings, err := ScanOrphans(e, nil, nil)
	if err != nil {
		t.Fatalf("the scan should not fail: %v", err)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "access is denied") {
		t.Fatalf("expected a warning naming the reason: %v", warnings)
	}
}

// A file that went away between the listing and the query is still worth
// reporting, without a size.
func TestScanOrphansReportsADiskItCouldNotMeasure(t *testing.T) {
	dir := `C:\Users\u\AppData\Local\wsl`
	sub := dir + `\{a}`
	e := orphanEnv(map[string][]DirEntry{
		dir: {{Path: sub, IsDir: true}},
		sub: {{Path: sub + `\ext4.vhdx`}},
	}, nil) // no file entries, so the size query fails

	got, _, err := ScanOrphans(e, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].SizeOnDisk != nil {
		t.Errorf("size should be absent, got %v", got[0].SizeOnDisk)
	}
}

func TestDeleteOrphansRefusesAFileThatIsOpen(t *testing.T) {
	held := `C:\d\held.vhdx`
	free := `C:\d\free.vhdx`
	fsys := &fakeFS{files: map[string]fakeFile{
		held: {onDisk: 1, locked: true},
		free: {onDisk: 1},
	}}
	e := Env{FS: fsys, Disks: &fakeDisks{}, Host: &fakeHost{}, Clock: &fakeClock{}}

	results := DeleteOrphans(e, []Orphan{{Path: held}, {Path: free}}, fsys.Remove)
	if len(results) != 2 {
		t.Fatalf("every file should be reported: %+v", results)
	}
	if results[0].Deleted || results[0].Err == nil {
		t.Errorf("the held file should have been refused: %+v", results[0])
	}
	// A refusal must not stop the loop.
	if !results[1].Deleted {
		t.Errorf("the free file should have gone: %+v", results[1])
	}
	if len(fsys.removed) != 1 || fsys.removed[0] != free {
		t.Errorf("removed %v", fsys.removed)
	}
}

// Not being able to answer whether a file is in use is not permission to delete
// it.
func TestDeleteOrphansRefusesWhenItCannotTellWhetherAFileIsOpen(t *testing.T) {
	missing := `C:\d\gone.vhdx`
	fsys := &fakeFS{}
	e := Env{FS: fsys, Disks: &fakeDisks{}, Host: &fakeHost{}, Clock: &fakeClock{}}
	results := DeleteOrphans(e, []Orphan{{Path: missing}}, fsys.Remove)
	if results[0].Deleted {
		t.Error("nothing should have been deleted")
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "could not be determined") {
		t.Errorf("the reason should be explicit: %+v", results[0])
	}
	if len(fsys.removed) != 0 {
		t.Errorf("nothing should have been removed: %v", fsys.removed)
	}
}

func TestRenderOrphans(t *testing.T) {
	var b bytes.Buffer
	RenderOrphans(&b, nil)
	if !strings.Contains(b.String(), "no orphaned disks found") {
		t.Errorf("got %q", b.String())
	}

	var b2 bytes.Buffer
	RenderOrphans(&b2, []Orphan{{Path: `C:\a.vhdx`, SizeOnDisk: u64(3 << 30)}, {Path: `C:\b.vhdx`}})
	out := b2.String()
	if !strings.Contains(out, "3.0 GiB") {
		t.Errorf("size missing:\n%s", out)
	}
	// An unmeasurable size is a dash, and the total counts what it could.
	if !strings.Contains(out, "-") {
		t.Errorf("unmeasured size should be a dash:\n%s", out)
	}
	if !strings.Contains(out, "in 2 file(s) that no distribution claims") {
		t.Errorf("total missing:\n%s", out)
	}
}

func TestOrphanJSONOmitsAnUnmeasurableSize(t *testing.T) {
	if _, ok := OrphanJSON(Orphan{Path: `C:\a.vhdx`})["size_on_disk"]; ok {
		t.Error("absent is absent, not zero")
	}
	if OrphanJSON(Orphan{Path: `C:\a.vhdx`, SizeOnDisk: u64(5)})["size_on_disk"] != uint64(5) {
		t.Error("a measured size should be reported")
	}
}
