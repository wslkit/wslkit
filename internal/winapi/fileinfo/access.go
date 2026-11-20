//go:build windows

package fileinfo

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procAccessCheck = windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")

// genericMapping is GENERIC_MAPPING for files.
type genericMapping struct {
	Read, Write, Execute, All uint32
}

var fileMapping = genericMapping{
	Read:    windows.FILE_GENERIC_READ,
	Write:   windows.FILE_GENERIC_WRITE,
	Execute: windows.FILE_GENERIC_EXECUTE,
	All:     fileAllAccess,
}

// fileAllAccess is FILE_ALL_ACCESS, which x/sys/windows does not define.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

// UserCanRead says whether the signed-in user, unelevated, may open path for
// reading. That is the token WSL uses: from 3.0.5 the service opens a
// distribution's disk, a custom kernel and a custom kernel-modules disk while
// impersonating the user, asking for GENERIC_READ (microsoft/WSL#42048).
//
// It asks the file's security descriptor rather than opening the file, so it
// answers the same whether wslkit runs elevated or not: when elevated, the
// check runs against the linked, unelevated token, the one an ordinary
// wsl.exe carries. Privileges such as SeBackupPrivilege do not count, as they
// do not for WSL.
func UserCanRead(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}

	var proc windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &proc); err != nil {
		return false, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer func() { _ = proc.Close() }()
	base := proc
	if proc.IsElevated() {
		// The linked token of an elevated one is the filtered token of the
		// same logon, which is what wsl.exe runs with from an ordinary
		// prompt. Without it, Administrators membership would hide exactly
		// the disks that fail for the user.
		if linked, err := proc.GetLinkedToken(); err == nil {
			defer func() { _ = linked.Close() }()
			base = linked
		}
	}
	var imp windows.Token
	if err := windows.DuplicateTokenEx(base, windows.TOKEN_QUERY, nil, windows.SecurityIdentification, windows.TokenImpersonation, &imp); err != nil {
		return false, fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	defer func() { _ = imp.Close() }()

	// PRIVILEGE_SET with room for a few entries; AccessCheck fills it with
	// the privileges it used, and none are expected for a read.
	var privs [64]byte
	privLen := uint32(len(privs))
	var granted uint32
	var status int32
	r, _, callErr := procAccessCheck.Call(
		uintptr(unsafe.Pointer(sd)),
		uintptr(imp),
		uintptr(windows.FILE_GENERIC_READ),
		uintptr(unsafe.Pointer(&fileMapping)),
		uintptr(unsafe.Pointer(&privs[0])),
		uintptr(unsafe.Pointer(&privLen)),
		uintptr(unsafe.Pointer(&granted)),
		uintptr(unsafe.Pointer(&status)),
	)
	if r == 0 {
		return false, fmt.Errorf("AccessCheck: %w", callErr)
	}
	return status != 0, nil
}
