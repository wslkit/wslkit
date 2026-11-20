//go:build windows

package fileinfo

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestUserCanRead(t *testing.T) {
	dir := t.TempDir()

	mine := filepath.Join(dir, "mine.vhdx")
	if err := os.WriteFile(mine, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := UserCanRead(mine); err != nil || !ok {
		t.Fatalf("own file: got %v, %v; want true", ok, err)
	}

	// A protected DACL that grants only SYSTEM: what a disk left behind by a
	// service, or copied with someone else's permissions, can look like.
	theirs := filepath.Join(dir, "theirs.vhdx")
	if err := os.WriteFile(theirs, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(theirs, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	// The owner keeps the right to change the DACL, so the test can clean up.
	t.Cleanup(func() {
		open, _ := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
		d, _, _ := open.DACL()
		_ = windows.SetNamedSecurityInfo(theirs, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, d, nil)
	})
	if ok, err := UserCanRead(theirs); err != nil || ok {
		t.Fatalf("SYSTEM-only file: got %v, %v; want false", ok, err)
	}
}
