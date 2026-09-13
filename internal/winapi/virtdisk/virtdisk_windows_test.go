//go:build windows

package virtdisk

import (
	"strings"
	"testing"
	"unsafe"
)

// The parameter blocks are validated by the kernel against the size implied by
// their version tag, so a wrong layout fails at runtime with a confusing
// ERROR_INVALID_PARAMETER rather than at compile time. Pin the sizes here
// against what virtdisk.h declares on 64-bit Windows.
func TestParameterBlockLayoutMatchesTheHeaders(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"OPEN_VIRTUAL_DISK_PARAMETERS v1", unsafe.Sizeof(openParametersV1{}), 8},
		{"OPEN_VIRTUAL_DISK_PARAMETERS v2", unsafe.Sizeof(openParametersV2{}), 28},
		{"COMPACT_VIRTUAL_DISK_PARAMETERS v1", unsafe.Sizeof(compactParametersV1{}), 8},
		{"ATTACH_VIRTUAL_DISK_PARAMETERS v1", unsafe.Sizeof(attachParametersV1{}), 8},
		{"GET_VIRTUAL_DISK_INFO size arm", unsafe.Sizeof(sizeInfoRaw{}), 32},
		{"VIRTUAL_DISK_PROGRESS", unsafe.Sizeof(virtualDiskProgress{}), 24},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s is %d bytes, want %d", c.name, c.got, c.want)
		}
	}
}

// The size arm carries eight bytes of header before the first 64-bit field.
// Getting the padding wrong reads the virtual size out of the wrong offset and
// reports a nonsense disk size.
func TestSizeArmFieldOffsets(t *testing.T) {
	var s sizeInfoRaw
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"VirtualSize", unsafe.Offsetof(s.VirtualSize), 8},
		{"PhysicalSize", unsafe.Offsetof(s.PhysicalSize), 16},
		{"BlockSize", unsafe.Offsetof(s.BlockSize), 24},
		{"SectorSize", unsafe.Offsetof(s.SectorSize), 28},
	} {
		if c.got != c.want {
			t.Errorf("%s is at offset %d, want %d", c.name, c.got, c.want)
		}
	}
	var p virtualDiskProgress
	if off := unsafe.Offsetof(p.CurrentValue); off != 8 {
		t.Errorf("CurrentValue is at offset %d, want 8", off)
	}
}

func TestVHDXStorageTypeIsTheMicrosoftProvider(t *testing.T) {
	st := VHDXStorageType()
	if st.DeviceID != StorageTypeDeviceVHDX {
		t.Errorf("device id %d, want %d", st.DeviceID, StorageTypeDeviceVHDX)
	}
	// VIRTUAL_STORAGE_TYPE_VENDOR_MICROSOFT.
	if got, want := st.VendorID.String(), "{EC984AEC-A0F9-47E9-901F-71415A66345B}"; got != want {
		t.Errorf("vendor %s, want %s", got, want)
	}
}

// A bad path must be rejected before the syscall, not passed through as a nil
// pointer.
func TestOpenRejectsAPathWithANulByte(t *testing.T) {
	if _, err := OpenForInfo("C:\\bad\x00path.vhdx"); err == nil {
		t.Fatal("expected an error for a path containing NUL")
	}
}

// A missing file must not look like a disk that is in use, or the command layer
// tells the user to stop a distribution that is already stopped.
func TestOpenAMissingFileIsNotReportedAsInUse(t *testing.T) {
	_, err := OpenForInfo(t.TempDir() + "\\definitely-absent.vhdx")
	if err == nil {
		t.Fatal("expected an error opening a file that is not there")
	}
	if isInUse(err) {
		t.Fatalf("a missing file was reported as in use: %v", err)
	}
}

func isInUse(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrInUse.Error())
}

// parentPathFrom decodes the variable-length tail of the parent-location
// payload, which the API need not terminate when the string exactly fills the
// buffer.
func TestParentPathDecoding(t *testing.T) {
	// Header is Version plus a BOOL, then UTF-16.
	build := func(s string, terminate bool) []byte {
		buf := make([]byte, 8)
		for _, r := range s {
			buf = append(buf, byte(r), 0)
		}
		if terminate {
			buf = append(buf, 0, 0)
		}
		return buf
	}
	if got := parentPathFrom(build("C:\\p.vhdx", true)); got != "C:\\p.vhdx" {
		t.Errorf("terminated: got %q", got)
	}
	if got := parentPathFrom(build("C:\\p.vhdx", false)); got != "C:\\p.vhdx" {
		t.Errorf("unterminated: got %q", got)
	}
	if got := parentPathFrom(make([]byte, 8)); got != "" {
		t.Errorf("header only: got %q", got)
	}
	if got := parentPathFrom(nil); got != "" {
		t.Errorf("empty: got %q", got)
	}
}
