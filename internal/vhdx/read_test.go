package vhdx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"strings"
	"testing"
)

// buildDataFixture makes a dynamic VHDX whose payload blocks really do live
// somewhere: each entry in blockAt gives the file offset the block is stored
// at, or 0 for a block that was never written. A block listed in partial is
// marked PARTIALLY_PRESENT, which is what a differencing disk looks like.
func buildDataFixture(t *testing.T, blockSize uint32, blockAt []int64, partial map[int]bool, fill func(block int, b []byte)) []byte {
	t.Helper()
	const batOff = 1 * 1024 * 1024
	const metaOff = 2 * 1024 * 1024
	size := int64(3 * 1024 * 1024)
	for _, at := range blockAt {
		if at+int64(blockSize) > size {
			size = at + int64(blockSize)
		}
	}
	img := make([]byte, size)
	copy(img[0:8], fileIDMagic)

	writeHeader := func(off int64, seq uint64) {
		h := img[off : off+sectorSize]
		copy(h[0:4], headerMagic)
		binary.LittleEndian.PutUint64(h[8:16], seq)
		binary.LittleEndian.PutUint32(h[4:8], 0)
		binary.LittleEndian.PutUint32(h[4:8], crc32.Checksum(h, castagnoli))
	}
	writeHeader(header1Offset, 1)
	writeHeader(header2Offset, 2)

	writeRegion := func(off int64) {
		rt := img[off : off+64*1024]
		copy(rt[0:4], regionMagic)
		binary.LittleEndian.PutUint32(rt[8:12], 2)
		e := rt[16:48]
		copy(e[0:16], guidBAT[:])
		binary.LittleEndian.PutUint64(e[16:24], batOff)
		binary.LittleEndian.PutUint32(e[24:28], 1024*1024)
		e = rt[48:80]
		copy(e[0:16], guidMetadata[:])
		binary.LittleEndian.PutUint64(e[16:24], metaOff)
		binary.LittleEndian.PutUint32(e[24:28], 1024*1024)
		binary.LittleEndian.PutUint32(rt[4:8], 0)
		binary.LittleEndian.PutUint32(rt[4:8], crc32.Checksum(rt, castagnoli))
	}
	writeRegion(regionTable1Off)
	writeRegion(regionTable2Off)

	mt := img[metaOff:]
	copy(mt[0:8], metadataMagic)
	binary.LittleEndian.PutUint16(mt[10:12], 3)
	items := []struct {
		g   guid
		val []byte
	}{
		{guidFileParameters, func() []byte { b := make([]byte, 8); binary.LittleEndian.PutUint32(b, blockSize); return b }()},
		{guidVirtualDiskSize, func() []byte {
			b := make([]byte, 8)
			binary.LittleEndian.PutUint64(b, uint64(len(blockAt))*uint64(blockSize))
			return b
		}()},
		{guidLogicalSectorSz, func() []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, 512); return b }()},
	}
	valOff := uint32(64 * 1024)
	for i, it := range items {
		e := mt[metadataHdrSize+i*metadataEntSize : metadataHdrSize+(i+1)*metadataEntSize]
		copy(e[0:16], it.g[:])
		binary.LittleEndian.PutUint32(e[16:20], valOff)
		binary.LittleEndian.PutUint32(e[20:24], uint32(len(it.val)))
		copy(mt[valOff:], it.val)
		valOff += 4096
	}

	chunkRatio := (uint64(1) << 23) * 512 / uint64(blockSize)
	bat := img[batOff:]
	idx := uint64(0)
	for block, at := range blockAt {
		if (idx+1)%(chunkRatio+1) == 0 {
			idx++ // sector bitmap entry
		}
		var entry uint64
		switch {
		case partial[block]:
			entry = uint64(at)&^0xfffff | payloadPartiallyPresent
		case at > 0:
			entry = uint64(at)&^0xfffff | payloadFullyPresent
		default:
			entry = payloadNotPresent
		}
		binary.LittleEndian.PutUint64(bat[idx*8:idx*8+8], entry)
		idx++
		if at > 0 && fill != nil {
			fill(block, img[at:at+int64(blockSize)])
		}
	}
	return img
}

// pattern is a per-block byte pattern: block 3 is all 0x03.
func pattern(block int, b []byte) {
	for i := range b {
		b[i] = byte(block)
	}
}

func openFixture(t *testing.T, img []byte) *Reader {
	t.Helper()
	d, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return d
}

func TestReadAtFindsBlocksWhereverTheyLanded(t *testing.T) {
	const bs = 1 << 20
	// Block 0 is stored after block 1, which is the whole point: a virtual
	// offset says nothing about where the bytes are in the file.
	img := buildDataFixture(t, bs, []int64{4 << 20, 3 << 20, 0}, nil, pattern)
	d := openFixture(t, img)

	for _, tc := range []struct {
		name string
		off  int64
		want byte
	}{
		{"block 0", 0, 0},
		{"inside block 0", 1024, 0},
		{"block 1", bs, 1},
		{"unallocated block reads as zeros", 2 * bs, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := make([]byte, 512)
			if _, err := d.ReadAt(b, tc.off); err != nil {
				t.Fatalf("ReadAt(%d): %v", tc.off, err)
			}
			if !bytes.Equal(b, bytes.Repeat([]byte{tc.want}, len(b))) {
				t.Fatalf("ReadAt(%d) = %x..., want all %02x", tc.off, b[:8], tc.want)
			}
		})
	}
}

func TestReadAtSpansBlocks(t *testing.T) {
	const bs = 1 << 20
	img := buildDataFixture(t, bs, []int64{3 << 20, 4 << 20}, nil, pattern)
	d := openFixture(t, img)

	b := make([]byte, 1024)
	n, err := d.ReadAt(b, bs-512)
	if err != nil || n != len(b) {
		t.Fatalf("ReadAt across the boundary: n=%d err=%v", n, err)
	}
	if !bytes.Equal(b[:512], make([]byte, 512)) {
		t.Error("first half should come from block 0")
	}
	if !bytes.Equal(b[512:], bytes.Repeat([]byte{1}, 512)) {
		t.Error("second half should come from block 1")
	}
}

func TestReadAtEndsAtTheEndOfTheDisk(t *testing.T) {
	const bs = 1 << 20
	img := buildDataFixture(t, bs, []int64{3 << 20}, nil, pattern)
	d := openFixture(t, img)

	b := make([]byte, 1024)
	n, err := d.ReadAt(b, bs-256)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if n != 256 {
		t.Errorf("n = %d, want the 256 bytes that exist", n)
	}
	if _, err := d.ReadAt(b, 8<<20); !errors.Is(err, io.EOF) {
		t.Errorf("reading past the virtual size: %v, want io.EOF", err)
	}
	if _, err := d.ReadAt(b, -1); err == nil {
		t.Error("a negative offset must be refused")
	}
}

// A differencing disk keeps half its sectors in a parent file. Guessing would
// mean handing back a mix of real data and zeros with no way to tell which.
func TestReadAtRefusesPartiallyPresentBlocks(t *testing.T) {
	const bs = 1 << 20
	img := buildDataFixture(t, bs, []int64{3 << 20}, map[int]bool{0: true}, pattern)
	d := openFixture(t, img)

	if _, err := d.ReadAt(make([]byte, 512), 0); err == nil || !strings.Contains(err.Error(), "partially present") {
		t.Fatalf("err = %v, want a refusal naming the partially present block", err)
	}
}

// A BAT entry is 44 bits of megabyte offset: a damaged one can point anywhere,
// including far past the end of a file this size.
func TestReadAtRejectsOffsetsOutsideTheFile(t *testing.T) {
	const bs = 1 << 20
	img := buildDataFixture(t, bs, []int64{3 << 20}, nil, pattern)
	d := openFixture(t, img)
	d.bat[0] = uint64(1<<40)&^0xfffff | payloadFullyPresent

	if _, err := d.ReadAt(make([]byte, 512), 0); err == nil || !strings.Contains(err.Error(), "outside the file") {
		t.Fatalf("err = %v, want a refusal naming the file bound", err)
	}
}

func TestParseStillWorksThroughOpen(t *testing.T) {
	img := buildFixture(t, 1<<20, 300, 120, false)
	info, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if info.AllocatedBytes != 120<<20 || info.BATEntries != 300 {
		t.Errorf("Parse after the refactor: allocated=%d entries=%d", info.AllocatedBytes, info.BATEntries)
	}
	// A file that is not a VHDX still returns an Info rather than nil, which
	// is what every caller of Parse relies on.
	bad, err := Parse(bytes.NewReader(make([]byte, 4096)), 4096)
	if err != ErrNotVHDX || bad == nil || bad.MagicOK {
		t.Errorf("Parse(not a vhdx) = %+v, %v", bad, err)
	}
}
