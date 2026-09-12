package vhdx

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// buildFixture makes a minimal, valid dynamic VHDX image in memory:
// virtual size = blocks × blockSize, with `full` blocks marked FULLY_PRESENT.
func buildFixture(t *testing.T, blockSize uint32, blocks uint64, full uint64, corruptHeader bool) []byte {
	t.Helper()
	const batOff = 1 * 1024 * 1024
	const metaOff = 2 * 1024 * 1024
	img := make([]byte, 3*1024*1024)
	copy(img[0:8], fileIDMagic)

	writeHeader := func(off int64, seq uint64) {
		h := img[off : off+sectorSize]
		copy(h[0:4], headerMagic)
		binary.LittleEndian.PutUint64(h[8:16], seq)
		binary.LittleEndian.PutUint32(h[4:8], 0)
		sum := crc32.Checksum(h, castagnoli)
		binary.LittleEndian.PutUint32(h[4:8], sum)
	}
	writeHeader(header1Offset, 1)
	writeHeader(header2Offset, 2)
	if corruptHeader {
		img[header2Offset+100] ^= 0xff
	}

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

	// Metadata table: file parameters, virtual disk size, logical sector size.
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
			binary.LittleEndian.PutUint64(b, blocks*uint64(blockSize))
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

	// BAT: chunkRatio = 2^23 * 512 / blockSize
	chunkRatio := (uint64(1) << 23) * 512 / uint64(blockSize)
	bat := img[batOff:]
	idx := uint64(0)
	payload := uint64(0)
	for payload < blocks {
		if (idx+1)%(chunkRatio+1) == 0 {
			idx++ // bitmap entry
			continue
		}
		state := byte(payloadNotPresent)
		if payload < full {
			state = payloadFullyPresent
		}
		bat[idx*8] = state
		idx++
		payload++
	}
	return img
}

func TestParseFixture(t *testing.T) {
	img := buildFixture(t, 1<<20, 300, 120, false)
	info, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.MagicOK || !info.HeaderOK {
		t.Fatal("magic/header not ok")
	}
	if info.HeaderSeq != 2 {
		t.Errorf("expected header seq 2, got %d", info.HeaderSeq)
	}
	if info.VirtualSize != 300<<20 {
		t.Errorf("virtual size %d", info.VirtualSize)
	}
	if info.BlockSize != 1<<20 {
		t.Errorf("block size %d", info.BlockSize)
	}
	if info.AllocatedBytes != 120<<20 {
		t.Errorf("allocated %d, want %d", info.AllocatedBytes, 120<<20)
	}
	if info.BATEntries != 300 {
		t.Errorf("bat entries %d", info.BATEntries)
	}
}

func TestCorruptSecondHeaderFallsBackToFirst(t *testing.T) {
	img := buildFixture(t, 1<<20, 10, 1, true)
	info, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if info.HeaderSeq != 1 {
		t.Errorf("expected fallback to header 1, got seq %d", info.HeaderSeq)
	}
}

func TestNotVHDX(t *testing.T) {
	_, err := Parse(bytes.NewReader(make([]byte, 4096)), 4096)
	if err != ErrNotVHDX {
		t.Fatalf("expected ErrNotVHDX, got %v", err)
	}
}

func TestLargeBlockCrossesBitmapEntries(t *testing.T) {
	// 32 MiB blocks -> chunkRatio 128; 300 blocks span three chunks.
	img := buildFixture(t, 32<<20, 300, 299, false)
	info, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if info.AllocatedBytes != 299*(32<<20) {
		t.Errorf("allocated %d", info.AllocatedBytes)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(buildFixture(&testing.T{}, 1<<20, 4, 2, false)[:300*1024])
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(bytes.NewReader(data), int64(len(data))) // must not panic
	})
}
