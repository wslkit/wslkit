//go:build windows

package peexport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKernel32Exports(t *testing.T) {
	dll := filepath.Join(os.Getenv("SystemRoot"), "System32", "kernel32.dll")
	names, err := Names(dll)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 1000 {
		t.Fatalf("kernel32 should export >1000 names, got %d", len(names))
	}
	has, err := Has(dll, "CreateFileW")
	if err != nil || !has {
		t.Fatalf("CreateFileW missing: %v", err)
	}
	has, _ = Has(dll, "WSLPluginAPI_EntryPointV1")
	if has {
		t.Fatal("kernel32 must not look like a WSL plugin")
	}
}

func TestNotPE(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.dll")
	if err := os.WriteFile(p, []byte("not a pe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Names(p); err == nil {
		t.Fatal("expected error for non-PE")
	}
}
