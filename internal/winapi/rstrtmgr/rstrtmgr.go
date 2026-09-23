//go:build windows

// Package rstrtmgr asks the Restart Manager which processes have a file open.
//
// It holds no handle on the file itself, so asking cannot get in the way of
// whoever is using it, and it needs no elevation. wslkit uses it to tell a
// running wslc session VM from a stopped one: while the VM runs, its disk is
// attached and held by System; while it is stopped, nobody holds it.
// Measured on WSL 2.9.12; see docs/research/2026-09-wslc-session.md.
package rstrtmgr

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mod              = windows.NewLazySystemDLL("rstrtmgr.dll")
	procStartSession = mod.NewProc("RmStartSession")
	procRegisterRes  = mod.NewProc("RmRegisterResources")
	procGetList      = mod.NewProc("RmGetList")
	procEndSession   = mod.NewProc("RmEndSession")
	errMoreData      = syscall.Errno(234) // ERROR_MORE_DATA
)

const (
	cchRmSessionKey     = 32
	cchRmMaxAppName     = 255
	cchRmMaxServiceName = 63
)

type uniqueProcess struct {
	PID       uint32
	StartTime windows.Filetime
}

type processInfo struct {
	Process          uniqueProcess
	AppName          [cchRmMaxAppName + 1]uint16
	ServiceShortName [cchRmMaxServiceName + 1]uint16
	ApplicationType  uint32
	AppStatus        uint32
	TSSessionID      uint32
	Restartable      int32
}

// Holder is one process with the file open.
type Holder struct {
	PID  uint32
	Name string
}

// Holders lists the processes that have path open. An empty list means
// nobody does.
func Holders(path string) ([]Holder, error) {
	var session uint32
	key := make([]uint16, cchRmSessionKey+1)
	if r, _, _ := procStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil, fmt.Errorf("rstrtmgr: RmStartSession: %w", syscall.Errno(r))
	}
	defer procEndSession.Call(uintptr(session)) //nolint:errcheck // nothing to do if ending fails

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	files := []*uint16{p}
	if r, _, _ := procRegisterRes.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&files[0])), 0, 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("rstrtmgr: RmRegisterResources: %w", syscall.Errno(r))
	}

	var needed, n, reasons uint32
	r, _, _ := procGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&n)), 0, uintptr(unsafe.Pointer(&reasons)))
	if r == 0 && needed == 0 {
		return nil, nil
	}
	if r != 0 && syscall.Errno(r) != errMoreData {
		return nil, fmt.Errorf("rstrtmgr: RmGetList: %w", syscall.Errno(r))
	}
	// The list can grow between the two calls; ask for a little more.
	info := make([]processInfo, needed+4)
	n = uint32(len(info))
	r, _, _ = procGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&reasons)))
	if r != 0 {
		return nil, fmt.Errorf("rstrtmgr: RmGetList: %w", syscall.Errno(r))
	}
	out := make([]Holder, 0, n)
	for _, pi := range info[:n] {
		out = append(out, Holder{PID: pi.Process.PID, Name: windows.UTF16ToString(pi.AppName[:])})
	}
	return out, nil
}
