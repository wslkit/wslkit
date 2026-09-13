//go:build windows

package disk

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// makeSparse writes a file with a hole in the middle: data at the start, a gap,
// then data again. This is the shape of a WSL disk, where most of the file is
// never written.
func makeSparse(t *testing.T, path string, holeSize int64) (first, second []byte) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var returned uint32
	if err := windows.DeviceIoControl(h, fsctlSetSparse, nil, 0, nil, 0, &returned, nil); err != nil {
		t.Skipf("this volume does not support sparse files: %v", err)
	}

	first = bytes.Repeat([]byte{0xAB}, 4096)
	second = bytes.Repeat([]byte{0xCD}, 4096)

	var written uint32
	if err := windows.WriteFile(h, first, &written, nil); err != nil {
		t.Fatal(err)
	}
	if err := seek(h, int64(len(first))+holeSize); err != nil {
		t.Fatal(err)
	}
	if err := windows.WriteFile(h, second, &written, nil); err != nil {
		t.Fatal(err)
	}
	return first, second
}

// A copy that filled in the holes would turn a WSL disk from the twelve
// gibibytes it occupies into the terabyte it is nominally allowed to reach.
func TestCopySparsePreservesHoles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.vhdx")
	dst := filepath.Join(dir, "dst.vhdx")
	const hole = 16 << 20

	first, second := makeSparse(t, src, hole)

	var fs WindowsFS
	srcLogical, err := fs.FileSize(src)
	if err != nil {
		t.Fatal(err)
	}
	srcOnDisk, err := fs.SizeOnDisk(src)
	if err != nil {
		t.Fatal(err)
	}
	if srcOnDisk >= srcLogical {
		t.Skipf("the fixture did not end up sparse on this volume (%d on disk, %d logical)", srcOnDisk, srcLogical)
	}

	var lastDone, lastTotal uint64
	if err := fs.CopySparse(src, dst, func(done, total uint64) bool {
		lastDone, lastTotal = done, total
		return true
	}); err != nil {
		t.Fatal(err)
	}

	// The copy must have the same logical length.
	dstLogical, err := fs.FileSize(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dstLogical != srcLogical {
		t.Errorf("logical size %d, want %d", dstLogical, srcLogical)
	}

	// And it must still be sparse: the hole was not written out.
	dstOnDisk, err := fs.SizeOnDisk(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dstOnDisk >= dstLogical {
		t.Errorf("the copy is not sparse: %d on disk of %d logical", dstOnDisk, dstLogical)
	}
	if sparse, err := fs.Sparse(dst); err != nil || !sparse {
		t.Errorf("the sparse attribute was not set: %v %v", sparse, err)
	}

	// Progress counts the real bytes, not the logical length.
	if lastTotal == 0 || lastTotal > srcLogical/2 {
		t.Errorf("progress total %d should be the allocated bytes, well under the logical %d", lastTotal, srcLogical)
	}
	if lastDone != lastTotal {
		t.Errorf("progress ended at %d of %d", lastDone, lastTotal)
	}

	// The data has to be where it was, and the hole has to read as zeroes.
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != int(srcLogical) {
		t.Fatalf("read %d bytes, want %d", len(got), srcLogical)
	}
	if !bytes.Equal(got[:len(first)], first) {
		t.Error("the first island did not survive")
	}
	off := len(first) + hole
	if !bytes.Equal(got[off:off+len(second)], second) {
		t.Error("the second island is not at the right offset")
	}
	if !bytes.Equal(got[len(first):off], make([]byte, hole)) {
		t.Error("the hole is not zero")
	}
}

// The likeliest thing sitting at the destination is the user's own previous
// attempt, so it is never overwritten.
func TestCopySparseRefusesAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.vhdx")
	dst := filepath.Join(dir, "dst.vhdx")
	makeSparse(t, src, 1<<20)
	if err := os.WriteFile(dst, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (WindowsFS{}).CopySparse(src, dst, nil); err == nil {
		t.Fatal("an existing destination should be refused")
	}
	// And the existing file must be untouched.
	if b, err := os.ReadFile(dst); err != nil || string(b) != "mine" {
		t.Errorf("the existing file was disturbed: %q %v", b, err)
	}
}

// A cancelled copy must not leave a half-written disk that looks finished.
func TestCopySparseRemovesItsPartialFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.vhdx")
	dst := filepath.Join(dir, "dst.vhdx")
	makeSparse(t, src, 1<<20)

	err := (WindowsFS{}).CopySparse(src, dst, func(done, total uint64) bool { return false })
	if err == nil {
		t.Fatal("expected the cancellation to be reported")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("the partial copy was left behind")
	}
}

func TestAllocatedBytesCountsOnlyRealData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.vhdx")
	makeSparse(t, src, 16<<20)

	var fs WindowsFS
	logical, err := fs.FileSize(src)
	if err != nil {
		t.Fatal(err)
	}
	allocated, err := fs.AllocatedBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if allocated == 0 || allocated >= logical {
		t.Errorf("allocated %d of logical %d: the hole should not be counted", allocated, logical)
	}
}

func TestSameVolume(t *testing.T) {
	dir := t.TempDir()
	same, err := (WindowsFS{}).SameVolume(dir, filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Error("a directory and its child are on the same volume")
	}
}

func TestMkdirAllIsHappyWithWhatAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	var fs WindowsFS
	if err := fs.MkdirAll(nested); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists(nested) {
		t.Fatal("the directory was not created")
	}
	// Running it again must not fail, and neither must a drive root, which
	// reports access denied rather than already-exists.
	if err := fs.MkdirAll(nested); err != nil {
		t.Errorf("a second call should be fine: %v", err)
	}
	if err := fs.MkdirAll(filepath.VolumeName(dir) + `\`); err != nil {
		t.Errorf("a drive root should be fine: %v", err)
	}
}
