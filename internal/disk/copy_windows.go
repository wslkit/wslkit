//go:build windows

package disk

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fsctlSetSparse is FSCTL_SET_SPARSE.
const fsctlSetSparse = 0x000900C4

// copyChunk is how much is moved per read and write.
//
// One mebibyte matches the VHDX block size WSL uses, so a chunk rarely straddles
// two blocks, and it is small enough that a cancelled copy stops promptly.
const copyChunk = 1 << 20

// Rename moves a file within a volume.
//
// MOVEFILE_COPY_ALLOWED is deliberately not set. Its cross-volume fallback is an
// ordinary copy, which would fill in every hole of a sparse disk and turn a
// 12 GiB file into a 1 TiB one.
func (WindowsFS) Rename(from, to string) error {
	f, err := widePath(from)
	if err != nil {
		return err
	}
	t, err := widePath(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(f, t, 0); err != nil {
		return fmt.Errorf("disk: moving %s to %s: %w", from, to, err)
	}
	return nil
}

// SameVolume reports whether two paths live on the same volume.
func (WindowsFS) SameVolume(a, b string) (bool, error) {
	rootA, err := volumeRoot(a)
	if err != nil {
		return false, err
	}
	rootB, err := volumeRoot(b)
	if err != nil {
		return false, err
	}
	return CanonicalPath(rootA) == CanonicalPath(rootB), nil
}

func volumeRoot(path string) (string, error) {
	p, err := widePath(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return "", fmt.Errorf("disk: GetVolumePathName(%s): %w", path, err)
	}
	return windows.UTF16ToString(buf), nil
}

// MkdirAll creates a directory and its parents.
func (WindowsFS) MkdirAll(path string) error {
	path = trimTrailingSep(path)
	if path == "" {
		return nil
	}
	// A drive root always exists, and CreateDirectory on one fails with
	// access denied rather than already-exists, which would look like a real
	// failure and sink every call that reached it.
	if isDriveRoot(path) {
		return nil
	}
	if parent := DirOf(path); parent != "" && !isDriveRoot(parent) {
		if err := (WindowsFS{}).MkdirAll(parent); err != nil {
			return err
		}
	}
	p, err := widePath(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(p, nil); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil
		}
		return fmt.Errorf("disk: creating %s: %w", path, err)
	}
	return nil
}

func trimTrailingSep(p string) string {
	for len(p) > 3 && (p[len(p)-1] == '\\' || p[len(p)-1] == '/') {
		p = p[:len(p)-1]
	}
	return p
}

func isDriveRoot(p string) bool {
	return len(p) == 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// allocatedRanges lists the parts of a file that hold real data.
func allocatedRanges(h windows.Handle, size uint64) ([]allocatedRange, error) {
	if size == 0 {
		return nil, nil
	}
	var out []allocatedRange
	query := allocatedRange{FileOffset: 0, Length: int64(size)}
	buf := make([]allocatedRange, 64)
	for {
		var returned uint32
		err := windows.DeviceIoControl(h, fsctlQueryAllocatedRanges,
			(*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
			(*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf)*int(unsafe.Sizeof(buf[0]))),
			&returned, nil)
		more := errors.Is(err, windows.ERROR_MORE_DATA)
		if err != nil && !more {
			return nil, fmt.Errorf("disk: querying allocated ranges: %w", err)
		}
		n := int(returned) / int(unsafe.Sizeof(buf[0]))
		out = append(out, buf[:n]...)
		if !more || n == 0 {
			return out, nil
		}
		last := buf[n-1]
		next := last.FileOffset + last.Length
		if next >= int64(size) {
			return out, nil
		}
		query = allocatedRange{FileOffset: next, Length: int64(size) - next}
	}
}

// CopySparse copies a virtual disk, preserving its holes.
//
// A plain byte-for-byte copy is not usable here. WSL creates these files sparse
// on current builds, and copying the logical length would write zeroes into
// every hole, turning a 12 GiB disk into the terabyte it is nominally allowed to
// grow to.
//
// The destination is created new: an existing file is an error rather than
// something to overwrite, because the likeliest thing sitting at that path is
// the user's own previous attempt.
func (WindowsFS) CopySparse(from, to string, progress func(done, total uint64) bool) (err error) {
	size, err := (WindowsFS{}).FileSize(from)
	if err != nil {
		return err
	}
	src, err := openRead(from)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(src) }()

	ranges, err := allocatedRanges(src, size)
	if err != nil {
		return err
	}
	var total uint64
	for _, r := range ranges {
		total += uint64(r.Length)
	}

	dst, err := createNew(to)
	if err != nil {
		return err
	}
	// On any failure the partial copy is removed here rather than left for
	// the caller's rollback: a half-written disk at the destination is the
	// thing most likely to be mistaken for a finished one.
	defer func() {
		cerr := windows.CloseHandle(dst)
		if err != nil {
			_ = (WindowsFS{}).Remove(to)
			return
		}
		if cerr != nil {
			err = fmt.Errorf("disk: closing %s: %w", to, cerr)
		}
	}()

	// Sparseness must be set before anything is written. Setting it
	// afterwards would not give back the space the writes had committed.
	var returned uint32
	if err := windows.DeviceIoControl(dst, fsctlSetSparse, nil, 0, nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("disk: making %s sparse: %w", to, err)
	}
	// Give the copy the same logical length. On a sparse file this costs
	// nothing and it keeps the two files the same size.
	if err := setLength(dst, int64(size)); err != nil {
		return err
	}

	buf := make([]byte, copyChunk)
	var done uint64
	for _, r := range ranges {
		offset := r.FileOffset
		remaining := r.Length
		for remaining > 0 {
			n := int64(len(buf))
			if remaining < n {
				n = remaining
			}
			// Both handles are positioned every chunk. The ranges are
			// not contiguous, and assuming they were would write the
			// second island over the hole after the first.
			if err := seek(src, offset); err != nil {
				return err
			}
			if err := seek(dst, offset); err != nil {
				return err
			}
			var read uint32
			if err := windows.ReadFile(src, buf[:n], &read, nil); err != nil {
				return fmt.Errorf("disk: reading %s: %w", from, err)
			}
			if read == 0 {
				return fmt.Errorf("disk: %s ended earlier than the filesystem said it would", from)
			}
			var written uint32
			if err := windows.WriteFile(dst, buf[:read], &written, nil); err != nil {
				return fmt.Errorf("disk: writing %s: %w", to, err)
			}
			if written != read {
				return fmt.Errorf("disk: %s accepted only part of a write", to)
			}
			offset += int64(read)
			remaining -= int64(read)
			done += uint64(read)
			if progress != nil && !progress(done, total) {
				return ErrCancelled
			}
		}
	}
	return nil
}

// ErrCancelled means a progress callback asked the copy to stop.
var ErrCancelled = errors.New("disk: cancelled")

func openRead(path string) (windows.Handle, error) {
	p, err := widePath(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, fmt.Errorf("disk: opening %s: %w", path, err)
	}
	return h, nil
}

func createNew(path string) (windows.Handle, error) {
	p, err := widePath(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0,
		nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, fmt.Errorf("disk: creating %s: %w", path, err)
	}
	return h, nil
}

// seek positions a handle at an absolute offset.
//
// The 64-bit form is always used. These files run to hundreds of gibibytes, and
// the 32-bit spelling would silently truncate the offset and copy the wrong
// part of the disk.
func seek(h windows.Handle, offset int64) error {
	high := int32(offset >> 32)
	low := int32(uint32(offset))
	if _, err := windows.SetFilePointer(h, low, &high, windows.FILE_BEGIN); err != nil {
		return fmt.Errorf("disk: seeking to %d: %w", offset, err)
	}
	return nil
}

func setLength(h windows.Handle, size int64) error {
	if err := seek(h, size); err != nil {
		return err
	}
	if err := windows.SetEndOfFile(h); err != nil {
		return fmt.Errorf("disk: setting the length to %d: %w", size, err)
	}
	return seek(h, 0)
}

// RemoveDir deletes an empty directory.
func (WindowsFS) RemoveDir(path string) error {
	p, err := widePath(path)
	if err != nil {
		return err
	}
	if err := windows.RemoveDirectory(p); err != nil {
		return fmt.Errorf("disk: removing the directory %s: %w", path, err)
	}
	return nil
}

// ReadFile reads a small file whole. Used for the trash manifest, never for a
// disk.
func (WindowsFS) ReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("disk: reading %s: %w", path, err)
	}
	return b, nil
}

// WriteFile writes a small file, replacing what was there.
func (WindowsFS) WriteFile(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("disk: writing %s: %w", path, err)
	}
	return nil
}
