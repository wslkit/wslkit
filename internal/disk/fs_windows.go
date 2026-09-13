//go:build windows

package disk

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WindowsFS is the production FileSystem.
type WindowsFS struct{}

var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetCompressedFileSze = modkernel32.NewProc("GetCompressedFileSizeW")
)

func widePath(path string) (*uint16, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("disk: bad path %q: %w", path, err)
	}
	return p, nil
}

// Exists reports whether a path is present.
//
// Only the errors that actually mean "not there" answer false. Anything else,
// such as a directory above the file denying traversal, answers true so the
// caller fails later with the real reason rather than being told the file is
// missing when it is not.
func (WindowsFS) Exists(path string) bool {
	p, err := widePath(path)
	if err != nil {
		return false
	}
	if _, err := windows.GetFileAttributes(p); err != nil {
		switch err {
		case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND,
			windows.ERROR_INVALID_NAME, windows.ERROR_BAD_NETPATH, windows.ERROR_BAD_NET_NAME:
			return false
		}
	}
	return true
}

// FileSize is the logical length of the file.
func (WindowsFS) FileSize(path string) (uint64, error) {
	p, err := widePath(path)
	if err != nil {
		return 0, err
	}
	var data windows.Win32FileAttributeData
	if err := windows.GetFileAttributesEx(p, windows.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&data))); err != nil {
		return 0, fmt.Errorf("disk: GetFileAttributesEx(%s): %w", path, err)
	}
	return uint64(data.FileSizeHigh)<<32 | uint64(data.FileSizeLow), nil
}

// SizeOnDisk is what the volume actually spends on the file, which is the
// number a user sees in Explorer and the one worth reporting. For a sparse or
// compressed file it is well below the logical length.
func (WindowsFS) SizeOnDisk(path string) (uint64, error) {
	if err := procGetCompressedFileSze.Find(); err != nil {
		return 0, fmt.Errorf("disk: GetCompressedFileSizeW unavailable: %w", err)
	}
	p, err := widePath(path)
	if err != nil {
		return 0, err
	}
	var high uint32
	// INVALID_FILE_SIZE is a legitimate low word for a file whose size is a
	// multiple of 4 GiB, so the last error must be consulted to tell a real
	// failure from a valid answer.
	low, _, callErr := procGetCompressedFileSze.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&high)),
	)
	if uint32(low) == 0xFFFFFFFF {
		if errno, ok := callErr.(windows.Errno); !ok || errno != 0 {
			return 0, fmt.Errorf("disk: GetCompressedFileSize(%s): %w", path, callErr)
		}
	}
	return uint64(high)<<32 | uint64(uint32(low)), nil
}

// Sparse reports the sparse attribute, which says only that the file may have
// holes, never how many bytes are real.
func (WindowsFS) Sparse(path string) (bool, error) {
	p, err := widePath(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return false, fmt.Errorf("disk: GetFileAttributes(%s): %w", path, err)
	}
	return attrs&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0, nil
}

// allocatedRange mirrors FILE_ALLOCATED_RANGE_BUFFER.
type allocatedRange struct {
	FileOffset int64
	Length     int64
}

// fsctlQueryAllocatedRanges is FSCTL_QUERY_ALLOCATED_RANGES.
const fsctlQueryAllocatedRanges = 0x000940CF

// AllocatedBytes sums the ranges of a sparse file that hold real data.
func (WindowsFS) AllocatedBytes(path string) (uint64, error) {
	size, err := WindowsFS{}.FileSize(path)
	if err != nil {
		return 0, err
	}
	if size == 0 {
		return 0, nil
	}
	p, err := widePath(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, fmt.Errorf("disk: opening %s to read its allocated ranges: %w", path, err)
	}
	defer windows.CloseHandle(h)

	var total uint64
	query := allocatedRange{FileOffset: 0, Length: int64(size)}
	out := make([]allocatedRange, 64)
	for {
		var returned uint32
		err := windows.DeviceIoControl(h, fsctlQueryAllocatedRanges,
			(*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
			(*byte)(unsafe.Pointer(&out[0])), uint32(len(out)*int(unsafe.Sizeof(out[0]))),
			&returned, nil)
		more := errors.Is(err, windows.ERROR_MORE_DATA)
		if err != nil && !more {
			return 0, fmt.Errorf("disk: querying the allocated ranges of %s: %w", path, err)
		}
		n := int(returned) / int(unsafe.Sizeof(out[0]))
		for i := 0; i < n; i++ {
			total += uint64(out[i].Length)
		}
		if !more {
			return total, nil
		}
		if n == 0 {
			// The filesystem asked for another pass but described
			// nothing. Continuing would spin forever.
			return total, nil
		}
		last := out[n-1]
		next := last.FileOffset + last.Length
		if next >= int64(size) {
			return total, nil
		}
		query = allocatedRange{FileOffset: next, Length: int64(size) - next}
	}
}

// Locked reports whether the file can be opened with no sharing. An error means
// the question could not be answered, which is not the same as a "no": the
// caller must not read a failure here as permission to delete something.
func (WindowsFS) Locked(path string) (bool, error) {
	p, err := widePath(path)
	if err != nil {
		return false, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err == nil {
		windows.CloseHandle(h)
		return false, nil
	}
	switch {
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return true, nil
	default:
		return false, fmt.Errorf("disk: checking whether %s is in use: %w", path, err)
	}
}

// Volume describes the volume a path sits on.
func (WindowsFS) Volume(path string) (VolumeInfo, error) {
	p, err := widePath(path)
	if err != nil {
		return VolumeInfo{}, err
	}
	root := make([]uint16, windows.MAX_PATH)
	if err := windows.GetVolumePathName(p, &root[0], uint32(len(root))); err != nil {
		return VolumeInfo{}, fmt.Errorf("disk: GetVolumePathName(%s): %w", path, err)
	}
	info := VolumeInfo{Root: windows.UTF16ToString(root)}

	var freeToCaller, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(&root[0], &freeToCaller, &total, &free); err != nil {
		return info, fmt.Errorf("disk: GetDiskFreeSpaceEx(%s): %w", info.Root, err)
	}
	// The quota-aware figure is the one that matters: it is what this user
	// can actually write.
	info.FreeBytes, info.TotalBytes = freeToCaller, total

	name := make([]uint16, windows.MAX_PATH)
	fsName := make([]uint16, windows.MAX_PATH)
	var serial, maxComponent, flags uint32
	if err := windows.GetVolumeInformation(&root[0], &name[0], uint32(len(name)),
		&serial, &maxComponent, &flags, &fsName[0], uint32(len(fsName))); err != nil {
		return info, fmt.Errorf("disk: GetVolumeInformation(%s): %w", info.Root, err)
	}
	info.FileSystem = windows.UTF16ToString(fsName)
	return info, nil
}

// List returns the entries of a directory matching a pattern.
//
// A directory that vanished mid-scan is an empty result, but a scan that ended
// for any other reason is an error: a drive pulled halfway through must not
// look like a complete answer.
func (WindowsFS) List(dir, pattern string) ([]DirEntry, error) {
	if pattern == "" {
		pattern = "*"
	}
	p, err := widePath(filepath.Join(dir, pattern))
	if err != nil {
		return nil, err
	}
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(p, &data)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_NO_MORE_FILES) ||
			errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, nil
		}
		return nil, fmt.Errorf("disk: listing %s: %w", dir, err)
	}
	defer func() { _ = windows.FindClose(h) }()

	var out []DirEntry
	for {
		name := windows.UTF16ToString(data.FileName[:])
		if name != "." && name != ".." {
			out = append(out, DirEntry{
				Path:  filepath.Join(dir, name),
				IsDir: data.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
			})
		}
		if err := windows.FindNextFile(h, &data); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return out, nil
			}
			return nil, fmt.Errorf("disk: listing %s: %w", dir, err)
		}
	}
}

// ExpandEnv expands %NAME% references.
func (WindowsFS) ExpandEnv(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.ExpandEnvironmentStrings(p, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", fmt.Errorf("disk: expanding %q: %w", s, err)
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n)
		if _, err := windows.ExpandEnvironmentStrings(p, &buf[0], n); err != nil {
			return "", fmt.Errorf("disk: expanding %q: %w", s, err)
		}
	}
	return windows.UTF16ToString(buf), nil
}

// Remove deletes a file.
func (WindowsFS) Remove(path string) error {
	p, err := widePath(path)
	if err != nil {
		return err
	}
	if err := windows.DeleteFile(p); err != nil {
		return fmt.Errorf("disk: deleting %s: %w", path, err)
	}
	return nil
}
