package ext4

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// The two fixtures are the first superblock of a 64 MiB image made with
// mke2fs -t ext4 (e2fsprogs 1.47, Ubuntu 26.04). errors.sb then had the error
// fields written with debugfs -R "ssv ...". Every expectation below is what
// dumpe2fs -h printed for the same file, so the test compares this parser
// against e2fsprogs rather than against itself:
//
//	Filesystem state:     not clean with errors
//	Errors behavior:      Continue
//	Block count:          16384
//	Block size:           4096
//	Mount count:          0
//	Maximum mount count:  -1
//	FS Error count:       7
//	First error time:     2026-09-01 12:00:00 UTC
//	First error function: ext4_lookup
//	First error line #:   1234
//	First error block #:  5678
//	Last error time:      2026-09-13 09:00:00 UTC
//	Last error function:  ext4_iget
//	Last error line #:    4321
func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != SuperSize {
		t.Fatalf("%s is %d bytes, want %d", name, len(b), SuperSize)
	}
	return b
}

func TestParseClean(t *testing.T) {
	s, err := ParseSuper(load(t, "clean.sb"))
	if err != nil {
		t.Fatalf("ParseSuper: %v", err)
	}
	if !s.Clean || s.HasErrors {
		t.Errorf("clean image: Clean=%v HasErrors=%v, want true/false", s.Clean, s.HasErrors)
	}
	if s.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d, want 0", s.ErrorCount)
	}
	if !s.First.Zero() || !s.Last.Zero() {
		t.Errorf("clean image records errors: first %+v last %+v", s.First, s.Last)
	}
	if s.BlockSize != 4096 || s.Blocks != 16384 {
		t.Errorf("BlockSize/Blocks = %d/%d, want 4096/16384", s.BlockSize, s.Blocks)
	}
	if s.OnError != "continue" {
		t.Errorf("OnError = %q, want continue", s.OnError)
	}
	if s.MaxMountCount != -1 {
		t.Errorf("MaxMountCount = %d, want -1", s.MaxMountCount)
	}
}

func TestParseErrors(t *testing.T) {
	s, err := ParseSuper(load(t, "errors.sb"))
	if err != nil {
		t.Fatalf("ParseSuper: %v", err)
	}
	if s.Clean || !s.HasErrors {
		t.Errorf("errored image: Clean=%v HasErrors=%v, want false/true", s.Clean, s.HasErrors)
	}
	if s.ErrorCount != 7 {
		t.Errorf("ErrorCount = %d, want 7", s.ErrorCount)
	}
	want := ErrorEvent{Time: 1788264000, Block: 5678, Func: "ext4_lookup", Line: 1234}
	if s.First != want {
		t.Errorf("First = %+v, want %+v", s.First, want)
	}
	want = ErrorEvent{Time: 1789290000, Func: "ext4_iget", Line: 4321}
	if s.Last != want {
		t.Errorf("Last = %+v, want %+v", s.Last, want)
	}
	if got := s.First.String(); got != "ext4_lookup:1234 block 5678, 2026-09-01 12:00 UTC" {
		t.Errorf("First.String() = %q", got)
	}
	if got := s.Last.String(); got != "ext4_iget:4321, 2026-09-13 09:00 UTC" {
		t.Errorf("Last.String() = %q", got)
	}
}

func TestParseRejects(t *testing.T) {
	t.Run("short", func(t *testing.T) {
		if _, err := ParseSuper(make([]byte, 512)); err == nil {
			t.Fatal("a half superblock must not parse")
		}
	})
	t.Run("no magic", func(t *testing.T) {
		b := load(t, "clean.sb")
		b[offMagic] = 0
		if _, err := ParseSuper(b); err != ErrNotExt4 {
			t.Fatalf("err = %v, want ErrNotExt4", err)
		}
	})
	t.Run("absurd block size", func(t *testing.T) {
		b := load(t, "clean.sb")
		binary.LittleEndian.PutUint32(b[offLogBlockSize:], 31)
		if _, err := ParseSuper(b); err == nil {
			t.Fatal("log block size 31 must not parse")
		}
	})
}

// A 64-bit filesystem keeps the high halves of the block counts in separate
// fields, and reading only the low half silently understates a large disk.
func TestParse64BitCounts(t *testing.T) {
	b := load(t, "clean.sb")
	binary.LittleEndian.PutUint32(b[offFeatureIncompat:], binary.LittleEndian.Uint32(b[offFeatureIncompat:])|incompat64Bit)
	binary.LittleEndian.PutUint32(b[offBlocksCountHi:], 2)
	binary.LittleEndian.PutUint32(b[offFreeBlocksHi:], 1)
	s, err := ParseSuper(b)
	if err != nil {
		t.Fatalf("ParseSuper: %v", err)
	}
	if s.Blocks != 2<<32|16384 {
		t.Errorf("Blocks = %d, want %d", s.Blocks, uint64(2)<<32|16384)
	}
	if s.FreeBlocks>>32 != 1 {
		t.Errorf("FreeBlocks = %d, want the high half set", s.FreeBlocks)
	}
}

// Function names come out of a filesystem that may be damaged, so a field full
// of junk must not become junk in the output.
func TestFuncFieldIsSanitised(t *testing.T) {
	b := load(t, "errors.sb")
	copy(b[offLastErrorFunc:offLastErrorFunc+errorFuncLen], append([]byte("ext4_\x07\x1bmb_gen"), bytes.Repeat([]byte{0}, 20)...))
	s, err := ParseSuper(b)
	if err != nil {
		t.Fatalf("ParseSuper: %v", err)
	}
	if s.Last.Func != "ext4_mb_gen" {
		t.Errorf("Func = %q, want the control bytes dropped", s.Last.Func)
	}
}
