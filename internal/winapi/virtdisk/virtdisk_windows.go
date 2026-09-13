//go:build windows

// Package virtdisk is a thin, hand-written binding for the Windows Virtual Disk
// Service API in virtdisk.dll: the subset wslkit needs to inspect, compact and
// attach a WSL 2 .vhdx.
//
// Everything is late-bound through LazyDLL so the binary still starts on a
// system without virtdisk.dll; callers get ErrUnavailable instead of a
// load-time crash. No cgo, per ADR 0001.
//
// Struct layout matters: these are passed to the kernel, so the fields are laid
// out exactly as virtdisk.h declares them and the version tag must match the
// union arm being filled in. The layout is pinned by tests.
package virtdisk

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modvirtdisk = windows.NewLazySystemDLL("virtdisk.dll")

	procOpenVirtualDisk             = modvirtdisk.NewProc("OpenVirtualDisk")
	procCompactVirtualDisk          = modvirtdisk.NewProc("CompactVirtualDisk")
	procAttachVirtualDisk           = modvirtdisk.NewProc("AttachVirtualDisk")
	procDetachVirtualDisk           = modvirtdisk.NewProc("DetachVirtualDisk")
	procGetVirtualDiskInformation   = modvirtdisk.NewProc("GetVirtualDiskInformation")
	procGetVirtualDiskOperationProg = modvirtdisk.NewProc("GetVirtualDiskOperationProgress")
	procGetVirtualDiskPhysicalPath  = modvirtdisk.NewProc("GetVirtualDiskPhysicalPath")
)

// ErrUnavailable is returned when virtdisk.dll or one of its entry points is
// missing. Callers degrade to reporting rather than failing hard.
var ErrUnavailable = errors.New("virtdisk: the Windows virtual disk API is not available on this system")

// ErrInUse reports that something else holds the VHD open, which for WSL means
// the distribution is running, the utility VM has not released it yet, or the
// file is attached.
var ErrInUse = errors.New("virtdisk: the virtual disk is in use")

// ErrCancelled reports that a progress callback asked for cancellation.
var ErrCancelled = errors.New("virtdisk: the operation was cancelled")

// available converts a load failure into ErrUnavailable.
func available(p *windows.LazyProc) error {
	if err := p.Find(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Available reports whether virtdisk.dll can be loaded at all.
func Available() bool { return modvirtdisk.Load() == nil }

// wrap turns the Win32 codes that have a clear operational meaning into
// sentinel errors, so command code can act on them without matching numbers.
func wrap(op, path string, e syscall.Errno) error {
	where := op
	if path != "" {
		where = op + "(" + path + ")"
	}
	switch e {
	case syscall.Errno(windows.ERROR_SHARING_VIOLATION), syscall.Errno(windows.ERROR_LOCK_VIOLATION):
		return fmt.Errorf("virtdisk: %s: %w: %v", where, ErrInUse, e)
	default:
		return fmt.Errorf("virtdisk: %s: %w", where, e)
	}
}

// StorageType identifies the container format. WSL only ever uses VHDX.
type StorageType struct {
	DeviceID uint32
	VendorID windows.GUID
}

// Device identifiers for VIRTUAL_STORAGE_TYPE.
const (
	StorageTypeDeviceUnknown = 0
	StorageTypeDeviceVHD     = 2
	StorageTypeDeviceVHDX    = 3
)

// vendorMicrosoft is VIRTUAL_STORAGE_TYPE_VENDOR_MICROSOFT.
var vendorMicrosoft = windows.GUID{
	Data1: 0xec984aec,
	Data2: 0xa0f9,
	Data3: 0x47e9,
	Data4: [8]byte{0x90, 0x1f, 0x71, 0x41, 0x5a, 0x66, 0x34, 0x5b},
}

// VHDXStorageType is the storage type for a .vhdx handled by the Microsoft
// provider.
func VHDXStorageType() StorageType {
	return StorageType{DeviceID: StorageTypeDeviceVHDX, VendorID: vendorMicrosoft}
}

// Access masks for the open calls (VIRTUAL_DISK_ACCESS_MASK).
const (
	// AccessNone is the only mask a version 2 open accepts. Rights come from
	// the parameter block instead, and any non-zero mask fails the open with
	// ERROR_INVALID_PARAMETER.
	AccessNone     = 0x00000000
	AccessAttachRO = 0x00010000
	AccessAttachRW = 0x00020000
	AccessDetach   = 0x00040000
	AccessGetInfo  = 0x00080000
	AccessCreate   = 0x00100000
	AccessMetaOps  = 0x00200000
	AccessRead     = 0x000d0000
	AccessAll      = 0x003f0000
)

// Flags for the open calls (OPEN_VIRTUAL_DISK_FLAG).
const (
	OpenFlagNone            = 0x00000000
	OpenFlagNoParents       = 0x00000001
	OpenFlagBlankFile       = 0x00000002
	OpenFlagBootDrive       = 0x00000004
	OpenFlagCachedIO        = 0x00000008
	OpenFlagCustomDiffChain = 0x00000010
	OpenFlagParentCachedIO  = 0x00000020
	OpenFlagVHDSetFileOnly  = 0x00000040
)

// openParametersV1 is OPEN_VIRTUAL_DISK_PARAMETERS with Version = 1. The V1
// union arm holds only RWDepth.
type openParametersV1 struct {
	Version uint32
	RWDepth uint32
}

// openParametersV2 is OPEN_VIRTUAL_DISK_PARAMETERS with Version = 2, whose
// union arm carries the access intent that the mask no longer expresses.
type openParametersV2 struct {
	Version        uint32
	GetInfoOnly    int32 // BOOL
	ReadOnly       int32 // BOOL
	ResiliencyGUID windows.GUID
}

// Flags for Compact (COMPACT_VIRTUAL_DISK_FLAG).
const (
	CompactFlagNone         = 0x00000000
	CompactFlagNoZeroScan   = 0x00000001
	CompactFlagNoBlockMoves = 0x00000002
)

type compactParametersV1 struct {
	Version  uint32
	Reserved uint32
}

// Flags for Attach (ATTACH_VIRTUAL_DISK_FLAG).
const (
	AttachFlagNone                 = 0x00000000
	AttachFlagReadOnly             = 0x00000001
	AttachFlagNoDriveLetter        = 0x00000002
	AttachFlagPermanentLifetime    = 0x00000004
	AttachFlagNoLocalHost          = 0x00000008
	AttachFlagNoSecurityDescriptor = 0x00000010
)

type attachParametersV1 struct {
	Version  uint32
	Reserved uint32
}

// DetachFlagNone is the only detach flag wslkit uses.
const DetachFlagNone = 0x00000000

// Versions for GetVirtualDiskInformation (GET_VIRTUAL_DISK_INFO_VERSION).
const (
	infoSize           = 1
	infoParentLocation = 3
	infoIsLoaded       = 13
)

// Handle is an open virtual disk. Close it with Close.
type Handle windows.Handle

// OpenForCompact opens a disk for compaction.
//
// The parameter shape is not a choice. Measured against Windows: a version 2
// block accepts VIRTUAL_DISK_ACCESS_NONE and nothing else, and any non-zero
// mask fails the open with ERROR_INVALID_PARAMETER rather than failing later,
// halfway through a compaction the user has already been told about. A version
// 1 block with AccessMetaOps also compacts without elevation, but it fails at
// the compaction rather than at the open, so version 2 is the safer spelling.
//
// A metadata operation needs the file to itself: while the distribution runs,
// the host compute service holds the disk and the open fails with a sharing
// violation, reported as ErrInUse.
func OpenForCompact(path string) (Handle, error) {
	params := openParametersV2{Version: 2}
	return open(path, AccessNone, OpenFlagNone, unsafe.Pointer(&params))
}

// OpenForInfo opens a disk to read its metadata and nothing else.
func OpenForInfo(path string) (Handle, error) {
	params := openParametersV2{Version: 2, GetInfoOnly: 1, ReadOnly: 1}
	return open(path, AccessNone, OpenFlagNone, unsafe.Pointer(&params))
}

// OpenForAttachReadOnly opens a disk so it can be surfaced read-only. This
// spelling keeps the version 1 block, whose RWDepth the attach path requires.
func OpenForAttachReadOnly(path string) (Handle, error) {
	params := openParametersV1{Version: 1, RWDepth: 1}
	return open(path, AccessAttachRO, OpenFlagNone, unsafe.Pointer(&params))
}

func open(path string, mask, flags uint32, params unsafe.Pointer) (Handle, error) {
	if err := available(procOpenVirtualDisk); err != nil {
		return 0, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("virtdisk: bad path %q: %w", path, err)
	}
	st := VHDXStorageType()
	var h windows.Handle
	r, _, _ := procOpenVirtualDisk.Call(
		uintptr(unsafe.Pointer(&st)),
		uintptr(unsafe.Pointer(p)),
		uintptr(mask),
		uintptr(flags),
		uintptr(params),
		uintptr(unsafe.Pointer(&h)),
	)
	if r != 0 {
		return 0, wrap("OpenVirtualDisk", path, syscall.Errno(r))
	}
	return Handle(h), nil
}

// Close releases the handle.
func (h Handle) Close() error { return windows.CloseHandle(windows.Handle(h)) }

// Progress reports how far a long-running operation has got.
type Progress struct {
	Current uint64
	Total   uint64
}

// ProgressFunc receives progress updates. Returning false asks for
// cancellation, which surfaces as ErrCancelled.
type ProgressFunc func(Progress) bool

// virtualDiskProgress is VIRTUAL_DISK_PROGRESS.
type virtualDiskProgress struct {
	OperationStatus uint32
	_               uint32
	CurrentValue    uint64
	CompletionValue uint64
}

// pollInterval is how often the compaction is asked how far it has got. A
// quarter second is frequent enough for a smooth percentage without making the
// wait itself measurable.
const pollInterval = 250 * time.Millisecond

// cancelPollInterval and cancelPollAttempts bound the wait for a cancelled
// operation to actually stop.
const (
	cancelPollInterval = 100 * time.Millisecond
	cancelPollAttempts = 100
)

// Compact reclaims unused blocks, reporting progress as it goes.
//
// The disk must have been opened with OpenForCompact and must not be attached.
// The guest filesystem must already have discarded the blocks: compacting
// without a prior TRIM reclaims almost nothing, because the VHDX still holds
// the stale data. Even after a TRIM the reclaim can be zero, because compaction
// works in whole VHDX blocks and free space scattered in small holes leaves
// every block partly occupied.
//
// The call runs asynchronously against an overlapped structure so progress can
// be polled. If the caller cancels, the operation is cancelled and waited for
// before returning: returning while the kernel still owns the overlapped
// structure would leave it writing into memory the caller has moved on from.
func (h Handle) Compact(flags uint32, progress ProgressFunc) error {
	if err := available(procCompactVirtualDisk); err != nil {
		return err
	}
	event, err := windows.CreateEvent(nil, 1 /* manual reset */, 0 /* unsignalled */, nil)
	if err != nil {
		return fmt.Errorf("virtdisk: CreateEvent: %w", err)
	}
	defer windows.CloseHandle(event)

	var ov windows.Overlapped
	ov.HEvent = event
	params := compactParametersV1{Version: 1}

	r, _, _ := procCompactVirtualDisk.Call(
		uintptr(h),
		uintptr(flags),
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Pointer(&ov)),
	)
	started := syscall.Errno(r)
	switch started {
	case 0:
		// Completed synchronously. Report one final tick so a caller
		// rendering a bar still sees it reach the end.
		report(progress, Progress{Current: 1, Total: 1})
		return nil
	case syscall.Errno(windows.ERROR_IO_PENDING):
	default:
		return wrap("CompactVirtualDisk", "", started)
	}

	for {
		waited, _ := windows.WaitForSingleObject(event, uint32(pollInterval.Milliseconds()))
		p, err := h.operationProgress()
		if err != nil {
			h.cancel(event)
			return err
		}
		if syscall.Errno(p.OperationStatus) != syscall.Errno(windows.ERROR_IO_PENDING) {
			if p.OperationStatus != 0 {
				return wrap("CompactVirtualDisk", "", syscall.Errno(p.OperationStatus))
			}
			report(progress, Progress{Current: p.CompletionValue, Total: p.CompletionValue})
			return nil
		}
		if !report(progress, Progress{Current: p.CurrentValue, Total: p.CompletionValue}) {
			h.cancel(event)
			return ErrCancelled
		}
		_ = waited
	}
}

// report calls the callback, treating a nil callback as "keep going".
func report(f ProgressFunc, p Progress) bool {
	if f == nil {
		return true
	}
	return f(p)
}

// cancel stops a pending operation and waits for the kernel to let go of the
// overlapped structure. Best effort: there is nothing useful to do if the
// cancellation itself fails, and returning early is the one thing that is
// unsafe.
func (h Handle) cancel(event windows.Handle) {
	_ = windows.CancelIoEx(windows.Handle(h), nil)
	for i := 0; i < cancelPollAttempts; i++ {
		if s, err := windows.WaitForSingleObject(event, uint32(cancelPollInterval.Milliseconds())); err == nil && s == windows.WAIT_OBJECT_0 {
			return
		}
		p, err := h.operationProgress()
		if err != nil || syscall.Errno(p.OperationStatus) != syscall.Errno(windows.ERROR_IO_PENDING) {
			return
		}
	}
}

func (h Handle) operationProgress() (virtualDiskProgress, error) {
	var p virtualDiskProgress
	if err := available(procGetVirtualDiskOperationProg); err != nil {
		return p, err
	}
	// The overlapped pointer identifies the operation. Passing nil asks
	// about the only one in flight, which is all wslkit ever starts.
	r, _, _ := procGetVirtualDiskOperationProg.Call(
		uintptr(h),
		0,
		uintptr(unsafe.Pointer(&p)),
	)
	if r != 0 {
		return p, wrap("GetVirtualDiskOperationProgress", "", syscall.Errno(r))
	}
	return p, nil
}

// Attach surfaces the disk as a physical drive. Attaching read-only with no
// drive letter is how wslkit inspects a disk without starting a distribution.
// It needs an elevated token: an unelevated caller gets ERROR_ACCESS_DENIED.
func (h Handle) Attach(flags uint32) error {
	if err := available(procAttachVirtualDisk); err != nil {
		return err
	}
	params := attachParametersV1{Version: 1}
	r, _, _ := procAttachVirtualDisk.Call(
		uintptr(h),
		0, // no security descriptor: inherit the default
		uintptr(flags),
		0, // provider-specific flags
		uintptr(unsafe.Pointer(&params)),
		0,
	)
	if r != 0 {
		return wrap("AttachVirtualDisk", "", syscall.Errno(r))
	}
	return nil
}

// AttachReadOnly attaches with the flag set wslkit uses everywhere: read-only,
// no drive letter, and not visible to the local host beyond this process.
func (h Handle) AttachReadOnly() error {
	return h.Attach(AttachFlagReadOnly | AttachFlagNoDriveLetter | AttachFlagNoLocalHost)
}

// Detach undoes Attach. An attached disk stays attached until reboot and blocks
// the distribution from starting, so this must run on every path out.
func (h Handle) Detach() error {
	if err := available(procDetachVirtualDisk); err != nil {
		return err
	}
	r, _, _ := procDetachVirtualDisk.Call(uintptr(h), DetachFlagNone, 0)
	if r != 0 {
		return wrap("DetachVirtualDisk", "", syscall.Errno(r))
	}
	return nil
}

// PhysicalPath returns the \\.\PhysicalDriveN path of an attached disk.
func (h Handle) PhysicalPath() (string, error) {
	if err := available(procGetVirtualDiskPhysicalPath); err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf) * 2)
	r, _, _ := procGetVirtualDiskPhysicalPath.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r != 0 {
		return "", wrap("GetVirtualDiskPhysicalPath", "", syscall.Errno(r))
	}
	return windows.UTF16ToString(buf), nil
}

// SizeInfo is the GET_VIRTUAL_DISK_INFO_SIZE payload.
//
// PhysicalSize is the provider's view and is not the same as what the file
// costs on the host volume, which is what a compressed-size query reports and
// what users actually notice.
type SizeInfo struct {
	VirtualSize  uint64
	PhysicalSize uint64
	BlockSize    uint32
	SectorSize   uint32
}

// sizeInfoRaw mirrors GET_VIRTUAL_DISK_INFO for the size arm. The union is
// 8-byte aligned, hence the padding after Version.
type sizeInfoRaw struct {
	Version      uint32
	_            uint32
	VirtualSize  uint64
	PhysicalSize uint64
	BlockSize    uint32
	SectorSize   uint32
}

// Size reads the virtual and physical size from the provider.
func (h Handle) Size() (SizeInfo, error) {
	if err := available(procGetVirtualDiskInformation); err != nil {
		return SizeInfo{}, err
	}
	var raw sizeInfoRaw
	raw.Version = infoSize
	size := uint32(unsafe.Sizeof(raw))
	var used uint32
	r, _, _ := procGetVirtualDiskInformation.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&raw)),
		uintptr(unsafe.Pointer(&used)),
	)
	if r != 0 {
		return SizeInfo{}, wrap("GetVirtualDiskInformation(size)", "", syscall.Errno(r))
	}
	return SizeInfo{
		VirtualSize:  raw.VirtualSize,
		PhysicalSize: raw.PhysicalSize,
		BlockSize:    raw.BlockSize,
		SectorSize:   raw.SectorSize,
	}, nil
}

// ParentLocation is the parent of a differencing disk. WSL never creates one,
// but a user might, and reporting it is how they find out why a disk they
// expected to be self-contained is not.
//
// An empty string with a nil error means the disk has no parent.
func (h Handle) ParentLocation() (string, error) {
	if err := available(procGetVirtualDiskInformation); err != nil {
		return "", err
	}
	// The payload ends in a variable-length string, so the call is made
	// twice: once to learn the size, once to read it.
	buf := make([]byte, 512)
	for attempt := 0; attempt < 2; attempt++ {
		// Version, then ParentResolved (BOOL), then the wide string. The
		// version tag is written into the front of the same buffer.
		*(*uint32)(unsafe.Pointer(&buf[0])) = infoParentLocation
		size := uint32(len(buf))
		var used uint32
		r, _, _ := procGetVirtualDiskInformation.Call(
			uintptr(h),
			uintptr(unsafe.Pointer(&size)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&used)),
		)
		switch syscall.Errno(r) {
		case 0:
			return parentPathFrom(buf), nil
		case syscall.Errno(windows.ERROR_NOT_FOUND):
			// Not a differencing disk, which is the normal case.
			return "", nil
		case syscall.Errno(windows.ERROR_INSUFFICIENT_BUFFER):
			if size <= uint32(len(buf)) {
				return "", fmt.Errorf("virtdisk: GetVirtualDiskInformation(parent) asked for %d bytes but %d were already given", size, len(buf))
			}
			buf = make([]byte, size)
		default:
			return "", wrap("GetVirtualDiskInformation(parent)", "", syscall.Errno(r))
		}
	}
	return "", errors.New("virtdisk: GetVirtualDiskInformation(parent) kept asking for a larger buffer")
}

// parentPathFrom decodes the trailing wide string of the parent-location
// payload. The API is not required to terminate the string when it exactly
// fills the buffer, so the capacity bounds the read rather than a NUL.
func parentPathFrom(buf []byte) string {
	const headerBytes = 8 // Version (4) + ParentResolved BOOL (4)
	if len(buf) <= headerBytes {
		return ""
	}
	rest := buf[headerBytes:]
	n := len(rest) / 2
	if n == 0 {
		return ""
	}
	u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&rest[0])), n)
	for i, c := range u16 {
		if c == 0 {
			return string(windows.UTF16ToString(u16[:i]))
		}
	}
	return windows.UTF16ToString(u16)
}

// IsLoaded reports whether the provider considers the disk attached. A loaded
// disk cannot be compacted.
func (h Handle) IsLoaded() (bool, error) {
	if err := available(procGetVirtualDiskInformation); err != nil {
		return false, err
	}
	var raw struct {
		Version  uint32
		IsLoaded int32 // BOOL
	}
	raw.Version = infoIsLoaded
	size := uint32(unsafe.Sizeof(raw))
	var used uint32
	r, _, _ := procGetVirtualDiskInformation.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&raw)),
		uintptr(unsafe.Pointer(&used)),
	)
	if r != 0 {
		return false, wrap("GetVirtualDiskInformation(is loaded)", "", syscall.Errno(r))
	}
	return raw.IsLoaded != 0, nil
}
