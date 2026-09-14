// Package ext4 reads an ext4 superblock and nothing else. It answers the one
// question a stopped distribution cannot be asked directly: does the kernel
// think this filesystem is sound, and if not, when did it stop being sound.
//
// The superblock records every error the kernel hit - a count, the first and
// the last, each with the function and line that reported it - and it survives
// a reboot, so the record of a corruption that made WSL fail to boot is still
// sitting in the file afterwards. Reading it costs one 1 KiB read at a fixed
// offset and starts nothing.
//
// Field offsets are from the kernel's struct ext4_super_block
// (Documentation/filesystems/ext4/super.rst).
package ext4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// SuperOffset is where the primary superblock sits in the filesystem:
	// 1024 bytes in, whatever the block size.
	SuperOffset = 1024
	// SuperSize is how much of it to read.
	SuperSize = 1024
	// magic is s_magic, the same for ext2, ext3 and ext4.
	magic = 0xEF53
)

// Superblock field offsets.
const (
	offBlocksCountLo   = 0x004
	offFreeBlocksLo    = 0x00C
	offLogBlockSize    = 0x018
	offMountCount      = 0x034
	offMaxMountCount   = 0x036
	offMagic           = 0x038
	offState           = 0x03A
	offErrorBehavior   = 0x03C
	offLastCheck       = 0x040
	offFeatureIncompat = 0x060
	offBlocksCountHi   = 0x150
	offFreeBlocksHi    = 0x158
	offErrorCount      = 0x194
	offFirstErrorTime  = 0x198
	offFirstErrorIno   = 0x19C
	offFirstErrorBlock = 0x1A0
	offFirstErrorFunc  = 0x1A8
	offFirstErrorLine  = 0x1C8
	offLastErrorTime   = 0x1CC
	offLastErrorIno    = 0x1D0
	offLastErrorLine   = 0x1D4
	offLastErrorBlock  = 0x1D8
	offLastErrorFunc   = 0x1E0
	errorFuncLen       = 32
	incompat64Bit      = 0x80
)

// s_state bits.
const (
	StateCleanlyUnmounted = 0x1
	StateErrors           = 0x2
	StateOrphansRecovered = 0x4
)

// Super is what the superblock says. Times are Unix seconds, zero when the
// kernel never recorded one.
type Super struct {
	// State is s_state as stored, so a caller can report the raw value.
	State uint16 `json:"state"`
	// Clean is the "cleanly unmounted" bit. A running filesystem is not
	// clean, and neither is one that went down with the VM: on its own the
	// bit says nothing about corruption.
	Clean bool `json:"clean"`
	// HasErrors is the "errors detected" bit. The kernel sets it and only
	// fsck clears it, so it outlives the boot that hit the error.
	HasErrors bool `json:"has_errors"`
	// OrphansRecovered means recovery of orphan inodes was interrupted.
	OrphansRecovered bool `json:"orphans_recovered,omitempty"`
	// OnError is what the kernel does when it hits one: continue,
	// remount-ro or panic.
	OnError string `json:"on_error,omitempty"`
	// ErrorCount is s_error_count: how many errors this filesystem has
	// recorded since the last fsck.
	ErrorCount uint32     `json:"error_count"`
	First      ErrorEvent `json:"first_error,omitzero"`
	Last       ErrorEvent `json:"last_error,omitzero"`
	// LastCheck is when fsck last ran, in Unix seconds.
	LastCheck int64 `json:"last_check,omitempty"`
	// MountCount and MaxMountCount are the mounts since that check, and the
	// count at which a check is forced (0 or -1 for never).
	MountCount    uint16 `json:"mount_count,omitempty"`
	MaxMountCount int16  `json:"max_mount_count,omitempty"`
	// BlockSize, Blocks and FreeBlocks describe the filesystem's size. The
	// free count is only as fresh as the last clean unmount.
	BlockSize  uint32 `json:"block_size,omitempty"`
	Blocks     uint64 `json:"blocks,omitempty"`
	FreeBlocks uint64 `json:"free_blocks,omitempty"`
}

// ErrorEvent is one recorded error: when, where, and which line of which
// kernel function reported it.
type ErrorEvent struct {
	Time  int64  `json:"time,omitempty"`
	Inode uint32 `json:"inode,omitempty"`
	Block uint64 `json:"block,omitempty"`
	Func  string `json:"func,omitempty"`
	Line  uint32 `json:"line,omitempty"`
}

// Zero reports whether nothing was recorded.
func (e ErrorEvent) Zero() bool { return e == ErrorEvent{} }

// String renders an event the way a person reads it: what reported it, where,
// and when.
func (e ErrorEvent) String() string {
	if e.Zero() {
		return ""
	}
	var b strings.Builder
	if e.Func != "" {
		b.WriteString(e.Func)
		if e.Line > 0 {
			fmt.Fprintf(&b, ":%d", e.Line)
		}
	}
	if e.Inode > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "inode %d", e.Inode)
	}
	if e.Block > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "block %d", e.Block)
	}
	if e.Time > 0 {
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(time.Unix(e.Time, 0).UTC().Format("2006-01-02 15:04 UTC"))
	}
	return b.String()
}

// ErrNotExt4 is returned when the magic is not there. On a WSL disk that means
// the file is not what it was expected to be, rather than that it is corrupt:
// the superblock magic is one of the last things a damaged filesystem loses.
var ErrNotExt4 = errors.New("ext4: superblock magic is not 0xEF53")

// ParseSuper reads a superblock out of b, which must be the SuperSize bytes
// found at SuperOffset in the filesystem.
func ParseSuper(b []byte) (Super, error) {
	var s Super
	if len(b) < SuperSize {
		return s, fmt.Errorf("ext4: superblock is %d bytes, want %d", len(b), SuperSize)
	}
	if binary.LittleEndian.Uint16(b[offMagic:]) != magic {
		return s, ErrNotExt4
	}
	s.State = binary.LittleEndian.Uint16(b[offState:])
	s.Clean = s.State&StateCleanlyUnmounted != 0
	s.HasErrors = s.State&StateErrors != 0
	s.OrphansRecovered = s.State&StateOrphansRecovered != 0
	switch binary.LittleEndian.Uint16(b[offErrorBehavior:]) {
	case 1:
		s.OnError = "continue"
	case 2:
		s.OnError = "remount-ro"
	case 3:
		s.OnError = "panic"
	}
	s.ErrorCount = binary.LittleEndian.Uint32(b[offErrorCount:])
	s.First = ErrorEvent{
		Time:  int64(binary.LittleEndian.Uint32(b[offFirstErrorTime:])),
		Inode: binary.LittleEndian.Uint32(b[offFirstErrorIno:]),
		Block: binary.LittleEndian.Uint64(b[offFirstErrorBlock:]),
		Func:  cstring(b[offFirstErrorFunc : offFirstErrorFunc+errorFuncLen]),
		Line:  binary.LittleEndian.Uint32(b[offFirstErrorLine:]),
	}
	s.Last = ErrorEvent{
		Time:  int64(binary.LittleEndian.Uint32(b[offLastErrorTime:])),
		Inode: binary.LittleEndian.Uint32(b[offLastErrorIno:]),
		Block: binary.LittleEndian.Uint64(b[offLastErrorBlock:]),
		Func:  cstring(b[offLastErrorFunc : offLastErrorFunc+errorFuncLen]),
		Line:  binary.LittleEndian.Uint32(b[offLastErrorLine:]),
	}
	s.LastCheck = int64(binary.LittleEndian.Uint32(b[offLastCheck:]))
	s.MountCount = binary.LittleEndian.Uint16(b[offMountCount:])
	s.MaxMountCount = int16(binary.LittleEndian.Uint16(b[offMaxMountCount:]))

	logBlockSize := binary.LittleEndian.Uint32(b[offLogBlockSize:])
	if logBlockSize > 16 { // 1024 << 16 is 64 MiB; nothing sane is near it
		return s, fmt.Errorf("ext4: log block size %d out of range", logBlockSize)
	}
	s.BlockSize = 1024 << logBlockSize
	s.Blocks = uint64(binary.LittleEndian.Uint32(b[offBlocksCountLo:]))
	s.FreeBlocks = uint64(binary.LittleEndian.Uint32(b[offFreeBlocksLo:]))
	if binary.LittleEndian.Uint32(b[offFeatureIncompat:])&incompat64Bit != 0 {
		s.Blocks |= uint64(binary.LittleEndian.Uint32(b[offBlocksCountHi:])) << 32
		s.FreeBlocks |= uint64(binary.LittleEndian.Uint32(b[offFreeBlocksHi:])) << 32
	}
	return s, nil
}

// cstring reads a NUL-padded fixed-width field. Anything that is not printable
// ASCII is dropped: these fields hold a kernel function name, and a byte that
// is not one is damage, not text to pass on.
func cstring(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		if c < 0x20 || c > 0x7e {
			continue
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
