// Package vhdx parses just enough of the VHDX on-disk format (MS-VHDX) to
// answer: is this file a VHDX, is a header valid, how large is the virtual
// disk, and how many payload blocks are allocated. It never mounts anything
// and reads a few hundred KiB at most.
package vhdx

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

const (
	fileIDOffset    = 0
	header1Offset   = 64 * 1024
	header2Offset   = 128 * 1024
	regionTable1Off = 192 * 1024
	regionTable2Off = 256 * 1024
	sectorSize      = 4096 // header structures are 4 KiB aligned

	fileIDMagic     = "vhdxfile"
	headerMagic     = "head"
	regionMagic     = "regi"
	metadataMagic   = "metadata"
	metadataHdrSize = 32
	metadataEntSize = 32
)

var (
	// Region GUIDs (little-endian mixed as stored on disk).
	guidBAT      = mustGUID("2DC27766-F623-4200-9D64-115E9BFD4A08")
	guidMetadata = mustGUID("8B7CA206-4790-4B9A-B8FE-575F050F886E")
	// Metadata item GUIDs.
	guidFileParameters   = mustGUID("CAA16737-FA36-4D43-B3B6-33F0AA44E76B")
	guidVirtualDiskSize  = mustGUID("2FA54224-CD1B-4876-B211-5DBED83BF4B8")
	guidLogicalSectorSz  = mustGUID("8141BF1D-A96F-4709-BA47-F233A8FAAB5F")
	guidPhysicalSectorSz = mustGUID("CDA348C7-445D-4471-9CC9-E9885251C556")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Info is what the parser returns.
type Info struct {
	MagicOK              bool
	HeaderOK             bool   // at least one header with valid checksum
	HeaderSeq            uint64 // sequence number of the header used
	VirtualSize          uint64
	BlockSize            uint32
	LogicalSector        uint32
	PhysicalSector       uint32
	LeaveBlocksAllocated bool // fixed-size VHDX
	HasParent            bool
	BATEntries           uint32
	AllocatedBytes       uint64 // payload blocks in state FULLY_PRESENT × BlockSize
	PartialBlocks        uint32 // PARTIALLY_PRESENT (only for differencing disks)
}

var ErrNotVHDX = errors.New("vhdx: file identifier is not 'vhdxfile'")

// Reader reads the contents of the virtual disk, not just its shape. A dynamic
// VHDX keeps payload blocks wherever they happened to land and never allocates
// the blocks nothing was written to, so reading at a virtual offset means
// walking the block allocation table; that table is what a Reader holds on to.
type Reader struct {
	r        io.ReaderAt
	info     *Info
	fileSize int64
	// bat is one entry per payload block, in block order, with the
	// sector-bitmap entries already dropped.
	bat []uint64
}

// Parse reads from an io.ReaderAt. fileSize bounds reads; pass 0 if unknown.
func Parse(r io.ReaderAt, fileSize int64) (*Info, error) {
	d, err := Open(r, fileSize)
	return d.Info(), err
}

// Open is Parse, keeping the block allocation table so the disk's contents can
// be read afterwards. Like Parse it returns what it managed to learn even when
// it fails, so a caller can report a half-readable file rather than nothing.
func Open(r io.ReaderAt, fileSize int64) (*Reader, error) {
	info := &Info{}
	d := &Reader{r: r, info: info, fileSize: fileSize}
	var id [8]byte
	if _, err := r.ReadAt(id[:], fileIDOffset); err != nil {
		return d, fmt.Errorf("vhdx: read file identifier: %w", err)
	}
	if string(id[:]) != fileIDMagic {
		return d, ErrNotVHDX
	}
	info.MagicOK = true

	h1, err1 := readHeader(r, header1Offset)
	h2, err2 := readHeader(r, header2Offset)
	var h *header
	switch {
	case err1 == nil && err2 == nil:
		if h2.seq > h1.seq {
			h = h2
		} else {
			h = h1
		}
	case err1 == nil:
		h = h1
	case err2 == nil:
		h = h2
	default:
		return d, fmt.Errorf("vhdx: both headers invalid: %v / %v", err1, err2)
	}
	info.HeaderOK = true
	info.HeaderSeq = h.seq

	regions, err := readRegionTable(r, regionTable1Off)
	if err != nil {
		regions, err = readRegionTable(r, regionTable2Off)
		if err != nil {
			return d, fmt.Errorf("vhdx: region tables: %w", err)
		}
	}
	var bat, meta *region
	for i := range regions {
		switch regions[i].guid {
		case guidBAT:
			bat = &regions[i]
		case guidMetadata:
			meta = &regions[i]
		}
	}
	if meta == nil {
		return d, errors.New("vhdx: no metadata region")
	}
	if err := readMetadata(r, meta, info); err != nil {
		return d, err
	}
	if bat != nil && info.BlockSize > 0 && info.VirtualSize > 0 {
		if err := readBAT(r, bat, info, &d.bat); err != nil {
			return d, err
		}
	}
	return d, nil
}

// Info is what the parse learned. It is never nil.
func (d *Reader) Info() *Info { return d.info }

// ReadAt reads at an offset in the virtual disk, which is not an offset in the
// file: block 3 of the guest's filesystem may sit anywhere, or nowhere. A block
// that was never written reads as zeros, which is exactly what the guest sees.
//
// Reading a differencing disk is refused rather than guessed at: a partially
// present block means the rest lives in a parent file this does not open.
func (d *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("vhdx: negative offset")
	}
	if d.info.BlockSize == 0 || len(d.bat) == 0 {
		return 0, errors.New("vhdx: no block allocation table was read")
	}
	blockSize := int64(d.info.BlockSize)
	n := 0
	for n < len(p) {
		vo := off + int64(n)
		if vo >= int64(d.info.VirtualSize) {
			return n, io.EOF
		}
		block := vo / blockSize
		if block >= int64(len(d.bat)) {
			return n, io.EOF
		}
		within := vo % blockSize
		want := len(p) - n
		if int64(want) > blockSize-within {
			want = int(blockSize - within)
		}
		entry := d.bat[block]
		switch entry & 0x7 {
		case payloadFullyPresent:
			at := int64(entry>>20)<<20 + within
			if at < 0 || (d.fileSize > 0 && at+int64(want) > d.fileSize) {
				return n, fmt.Errorf("vhdx: block %d points outside the file", block)
			}
			if _, err := d.r.ReadAt(p[n:n+want], at); err != nil {
				return n, fmt.Errorf("vhdx: read block %d: %w", block, err)
			}
		case payloadPartiallyPresent:
			return n, fmt.Errorf("vhdx: block %d is only partially present (differencing disk)", block)
		default:
			clear(p[n : n+want])
		}
		n += want
	}
	return n, nil
}

type header struct {
	seq uint64
}

func readHeader(r io.ReaderAt, off int64) (*header, error) {
	buf := make([]byte, sectorSize)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, err
	}
	if string(buf[0:4]) != headerMagic {
		return nil, fmt.Errorf("bad header magic at %d", off)
	}
	stored := binary.LittleEndian.Uint32(buf[4:8])
	// Checksum is computed with the checksum field zeroed.
	buf[4], buf[5], buf[6], buf[7] = 0, 0, 0, 0
	if crc32.Checksum(buf, castagnoli) != stored {
		return nil, fmt.Errorf("header checksum mismatch at %d", off)
	}
	return &header{seq: binary.LittleEndian.Uint64(buf[8:16])}, nil
}

type region struct {
	guid   guid
	offset uint64
	length uint32
}

func readRegionTable(r io.ReaderAt, off int64) ([]region, error) {
	buf := make([]byte, 64*1024)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, err
	}
	if string(buf[0:4]) != regionMagic {
		return nil, fmt.Errorf("bad region table magic at %d", off)
	}
	stored := binary.LittleEndian.Uint32(buf[4:8])
	buf[4], buf[5], buf[6], buf[7] = 0, 0, 0, 0
	if crc32.Checksum(buf, castagnoli) != stored {
		return nil, fmt.Errorf("region table checksum mismatch at %d", off)
	}
	count := binary.LittleEndian.Uint32(buf[8:12])
	if count > 2047 {
		return nil, fmt.Errorf("region table entry count %d out of range", count)
	}
	out := make([]region, 0, count)
	for i := uint32(0); i < count; i++ {
		e := buf[16+i*32 : 16+(i+1)*32]
		var g guid
		copy(g[:], e[0:16])
		out = append(out, region{
			guid:   g,
			offset: binary.LittleEndian.Uint64(e[16:24]),
			length: binary.LittleEndian.Uint32(e[24:28]),
		})
	}
	return out, nil
}

func readMetadata(r io.ReaderAt, m *region, info *Info) error {
	if m.length < 64*1024 || m.length > 16*1024*1024 {
		return fmt.Errorf("vhdx: metadata region length %d out of range", m.length)
	}
	hdr := make([]byte, 64*1024)
	if _, err := r.ReadAt(hdr, int64(m.offset)); err != nil {
		return fmt.Errorf("vhdx: read metadata table: %w", err)
	}
	if string(hdr[0:8]) != metadataMagic {
		return errors.New("vhdx: bad metadata table signature")
	}
	count := binary.LittleEndian.Uint16(hdr[10:12])
	if count > 2047 {
		return fmt.Errorf("vhdx: metadata entry count %d out of range", count)
	}
	for i := 0; i < int(count); i++ {
		e := hdr[metadataHdrSize+i*metadataEntSize : metadataHdrSize+(i+1)*metadataEntSize]
		var g guid
		copy(g[:], e[0:16])
		off := binary.LittleEndian.Uint32(e[16:20])
		length := binary.LittleEndian.Uint32(e[20:24])
		if length > 4096 || uint64(off)+uint64(length) > uint64(m.length) {
			continue
		}
		val := make([]byte, length)
		if _, err := r.ReadAt(val, int64(m.offset)+int64(off)); err != nil {
			return fmt.Errorf("vhdx: read metadata item: %w", err)
		}
		switch g {
		case guidFileParameters:
			if len(val) >= 8 {
				info.BlockSize = binary.LittleEndian.Uint32(val[0:4])
				flags := binary.LittleEndian.Uint32(val[4:8])
				info.LeaveBlocksAllocated = flags&1 != 0
				info.HasParent = flags&2 != 0
			}
		case guidVirtualDiskSize:
			if len(val) >= 8 {
				info.VirtualSize = binary.LittleEndian.Uint64(val[0:8])
			}
		case guidLogicalSectorSz:
			if len(val) >= 4 {
				info.LogicalSector = binary.LittleEndian.Uint32(val[0:4])
			}
		case guidPhysicalSectorSz:
			if len(val) >= 4 {
				info.PhysicalSector = binary.LittleEndian.Uint32(val[0:4])
			}
		}
	}
	if info.BlockSize == 0 || info.VirtualSize == 0 {
		return errors.New("vhdx: metadata missing file parameters or virtual disk size")
	}
	return nil
}

// BAT entry states (low 3 bits).
const (
	payloadNotPresent       = 0
	payloadUndefined        = 1
	payloadZero             = 2
	payloadUnmapped         = 3
	payloadFullyPresent     = 6
	payloadPartiallyPresent = 7
)

// readBAT walks the block allocation table. When keep is non-nil the payload
// entries are kept, in block order, so the disk's contents can be read later.
func readBAT(r io.ReaderAt, b *region, info *Info, keep *[]uint64) error {
	if info.LogicalSector == 0 {
		info.LogicalSector = 512
	}
	chunkRatio := (uint64(1) << 23) * uint64(info.LogicalSector) / uint64(info.BlockSize)
	if chunkRatio == 0 {
		return errors.New("vhdx: chunk ratio is zero")
	}
	dataBlocks := (info.VirtualSize + uint64(info.BlockSize) - 1) / uint64(info.BlockSize)
	// Total entries interleave one sector-bitmap entry after every chunkRatio payload entries.
	totalEntries := dataBlocks + (dataBlocks-1)/chunkRatio
	if b.length < 8 || uint64(b.length)/8 < totalEntries {
		return fmt.Errorf("vhdx: BAT region too small (%d bytes for %d entries)", b.length, totalEntries)
	}
	if totalEntries > 32*1024*1024 { // 256 MiB of BAT; far beyond any WSL disk
		return fmt.Errorf("vhdx: BAT has %d entries, refusing", totalEntries)
	}
	buf := make([]byte, totalEntries*8)
	if _, err := r.ReadAt(buf, int64(b.offset)); err != nil {
		return fmt.Errorf("vhdx: read BAT: %w", err)
	}
	var full, partial uint64
	var payloadIdx uint64
	if keep != nil {
		*keep = make([]uint64, 0, dataBlocks)
	}
	for i := uint64(0); i < totalEntries; i++ {
		// Every (chunkRatio+1)-th entry is a sector bitmap entry; skip it.
		if (i+1)%(chunkRatio+1) == 0 {
			continue
		}
		payloadIdx++
		entry := binary.LittleEndian.Uint64(buf[i*8 : i*8+8])
		if keep != nil {
			*keep = append(*keep, entry)
		}
		state := entry & 0x7
		switch state {
		case payloadFullyPresent:
			full++
		case payloadPartiallyPresent:
			partial++
		}
	}
	info.BATEntries = uint32(payloadIdx)
	info.AllocatedBytes = full * uint64(info.BlockSize)
	info.PartialBlocks = uint32(partial)
	return nil
}

type guid [16]byte

// mustGUID parses a canonical string GUID into its on-disk (mixed-endian) layout.
func mustGUID(s string) guid {
	var g guid
	var d1 uint32
	var d2, d3 uint16
	var d4 [8]byte
	n, err := fmt.Sscanf(s, "%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		&d1, &d2, &d3, &d4[0], &d4[1], &d4[2], &d4[3], &d4[4], &d4[5], &d4[6], &d4[7])
	if err != nil || n != 11 {
		panic("vhdx: bad guid literal " + s)
	}
	binary.LittleEndian.PutUint32(g[0:4], d1)
	binary.LittleEndian.PutUint16(g[4:6], d2)
	binary.LittleEndian.PutUint16(g[6:8], d3)
	copy(g[8:], d4[:])
	return g
}
