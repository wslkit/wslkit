//go:build windows

// Package fileinfo reads PE file versions, NTFS attributes, owners, and volume
// free space without touching file contents.
package fileinfo

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Version returns the FileVersion of a PE as "a.b.c.d".
func Version(path string) (string, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		return "", err
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return "", err
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var fixedLen uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &fixedLen); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d.%d",
		fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff,
		fixed.FileVersionLS>>16, fixed.FileVersionLS&0xffff), nil
}

type Attrs struct {
	Sparse, Compressed, Encrypted bool
}

func Attributes(path string) (Attrs, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Attrs{}, err
	}
	a, err := windows.GetFileAttributes(p)
	if err != nil {
		return Attrs{}, err
	}
	return Attrs{
		Sparse:     a&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0,
		Compressed: a&windows.FILE_ATTRIBUTE_COMPRESSED != 0,
		Encrypted:  a&windows.FILE_ATTRIBUTE_ENCRYPTED != 0,
	}, nil
}

// OwnerSID returns the owner SID string of a file.
func OwnerSID(path string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", err
	}
	return owner.String(), nil
}

// VolumeFree returns free bytes available to the caller on the volume holding path.
func VolumeFree(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return free, nil
}

// OpenShared opens a file read-only while allowing other processes full sharing,
// so a VHDX attached to a running VM can still be inspected.
func OpenShared(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}
