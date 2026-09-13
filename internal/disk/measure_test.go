package disk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func env(fs *fakeFS, disks *fakeDisks, host *fakeHost) Env {
	if fs == nil {
		fs = &fakeFS{}
	}
	if disks == nil {
		disks = &fakeDisks{}
	}
	if host == nil {
		host = &fakeHost{}
	}
	return Env{FS: fs, Disks: disks, Host: host, Clock: &fakeClock{}}
}

const ubuntuVhd = `C:\wsl\Ubuntu\ext4.vhdx`

func TestMeasureCollectsEverySource(t *testing.T) {
	fs := &fakeFS{files: map[string]fakeFile{
		ubuntuVhd: {size: 15 << 30, onDisk: 9 << 30, allocated: 9 << 30, sparse: true},
	}}
	disks := &fakeDisks{facts: map[string]DiskFacts{
		ubuntuVhd: {VirtualSize: 1 << 40, BlockSize: 2 << 20, SectorSize: 512},
	}}
	host := &fakeHost{results: map[string]CommandResult{
		"Ubuntu /bin/df": {Stdout: "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sdc 1000000000000 8589934592 900000000000 1% /\n"},
	}}
	info := Measure(context.Background(), env(fs, disks, host), reg("Ubuntu"), MeasureOptions{Running: map[string]bool{"Ubuntu": true}})

	if info.FileSize == nil || *info.FileSize != 15<<30 {
		t.Errorf("file size: %v", info.FileSize)
	}
	if info.SizeOnDisk == nil || *info.SizeOnDisk != 9<<30 {
		t.Errorf("size on disk: %v", info.SizeOnDisk)
	}
	if info.VirtualSize == nil || *info.VirtualSize != 1<<40 {
		t.Errorf("virtual size: %v", info.VirtualSize)
	}
	if info.GuestUsed == nil || *info.GuestUsed != 8589934592 {
		t.Errorf("guest used: %v", info.GuestUsed)
	}
	if info.Sparse == nil || !*info.Sparse {
		t.Errorf("sparse: %v", info.Sparse)
	}
	if len(info.Notes) != 0 {
		t.Errorf("unexpected notes: %v", info.Notes)
	}
}

// A missing disk must produce one actionable note, not one failure per
// measurement that happens to read the same file.
func TestMeasureAMissingDiskSaysSoOnce(t *testing.T) {
	info := Measure(context.Background(), env(nil, nil, nil), reg("Ubuntu"), MeasureOptions{})
	if len(info.Notes) != 1 {
		t.Fatalf("want exactly one note, got %v", info.Notes)
	}
	if !strings.Contains(info.Notes[0], "relink") {
		t.Errorf("the note should say what to do about it: %q", info.Notes[0])
	}
	if info.FileSize != nil || info.VirtualSize != nil {
		t.Error("nothing should have been measured")
	}
}

// A running distribution holds its disk open. That is the normal state of a
// working machine, so it is a missing measurement with an explanation, never a
// failed command.
func TestMeasureADiskHeldOpenIsANoteNotAFailure(t *testing.T) {
	fs := &fakeFS{files: map[string]fakeFile{ubuntuVhd: {size: 100, onDisk: 100}}}
	disks := &fakeDisks{err: map[string]error{ubuntuVhd: errors.New("the process cannot access the file")}}
	info := Measure(context.Background(), env(fs, disks, nil), reg("Ubuntu"), MeasureOptions{})
	if info.SizeOnDisk == nil {
		t.Error("the filesystem measurements should still have been taken")
	}
	if info.VirtualSize != nil {
		t.Error("the provider measurement should be absent")
	}
	if len(info.Notes) != 1 || !strings.Contains(info.Notes[0], "running") {
		t.Errorf("want one note explaining why, got %v", info.Notes)
	}
}

// Measuring must not boot anything. Starting a distribution to measure it
// changes the thing being measured.
func TestMeasureDoesNotStartAStoppedDistroUnlessAsked(t *testing.T) {
	fs := &fakeFS{files: map[string]fakeFile{ubuntuVhd: {size: 100, onDisk: 100}}}
	host := &fakeHost{}
	Measure(context.Background(), env(fs, nil, host), reg("Ubuntu"), MeasureOptions{Running: map[string]bool{}})
	if len(host.calls) != 0 {
		t.Fatalf("nothing should have run in the guest, got %v", host.calls)
	}

	host2 := &fakeHost{}
	Measure(context.Background(), env(fs, nil, host2), reg("Ubuntu"), MeasureOptions{Probe: true})
	if len(host2.calls) != 1 || !strings.Contains(host2.calls[0], "/bin/df") {
		t.Fatalf("--probe should have run df, got %v", host2.calls)
	}
}

func TestMeasureSkipsWSL1Entirely(t *testing.T) {
	host := &fakeHost{}
	info := Measure(context.Background(), env(nil, nil, host), reg("Legacy", func(r *Registration) { r.Version = 1 }), MeasureOptions{Probe: true})
	if len(info.Notes) != 0 || info.FileSize != nil {
		t.Errorf("a WSL 1 distribution has no disk to measure: %+v", info)
	}
	if len(host.calls) != 0 {
		t.Errorf("nothing should have run: %v", host.calls)
	}
}

func TestReclaimable(t *testing.T) {
	if got := (Info{SizeOnDisk: u64(100), GuestUsed: u64(40)}).Reclaimable(); got == nil || *got != 60 {
		t.Errorf("plain case: %v", got)
	}
	if got := (Info{SizeOnDisk: u64(100)}).Reclaimable(); got != nil {
		t.Errorf("without guest usage it is unknowable, got %v", got)
	}
	// On a compressed volume the guest figure can exceed the file's cost.
	if got := (Info{SizeOnDisk: u64(40), GuestUsed: u64(100)}).Reclaimable(); got == nil || *got != 0 {
		t.Errorf("must floor at zero, got %v", got)
	}
}

func TestParseDF(t *testing.T) {
	used, avail, err := ParseDF("Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sdc 1000 400 600 40% /\n")
	if err != nil {
		t.Fatal(err)
	}
	if used != 400 || avail != 600 {
		t.Errorf("got used=%d avail=%d", used, avail)
	}
}

// A long device name wraps onto its own line, leaving the numbers on the next
// one. Counting columns from the right survives that; counting from the left
// does not.
func TestParseDFHandlesAWrappedDeviceName(t *testing.T) {
	out := "Filesystem     1B-blocks  Used Available Use% Mounted on\n" +
		"/dev/disk/by-uuid/a-very-long-name-that-wraps\n" +
		"           1000 400 600 40% /\n"
	used, avail, err := ParseDF(out)
	if err != nil {
		t.Fatal(err)
	}
	if used != 400 || avail != 600 {
		t.Errorf("got used=%d avail=%d", used, avail)
	}
}

func TestParseDFRejectsWhatItCannotUnderstand(t *testing.T) {
	for _, in := range []string{"", "\n\n", "not enough columns"} {
		if _, _, err := ParseDF(in); err == nil {
			t.Errorf("ParseDF(%q) should have failed", in)
		}
	}
}
