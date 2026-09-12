//go:build windows

package authenticode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifySystemBinaryAndUnsignedFile(t *testing.T) {
	kind, hr := Verify(filepath.Join(os.Getenv("SystemRoot"), "System32", "kernel32.dll"))
	// kernel32 is catalog-signed; WinVerifyTrust reports it trusted on a healthy machine.
	if kind != "trusted" {
		t.Logf("kernel32: %s (0x%08X) - catalog signing state depends on the host", kind, hr)
	}
	p := filepath.Join(t.TempDir(), "x.dll")
	if err := os.WriteFile(p, []byte("MZ not really"), 0o600); err != nil {
		t.Fatal(err)
	}
	kind, _ = Verify(p)
	if kind != "unsigned" && kind[:5] != "error" {
		t.Fatalf("junk file -> %s", kind)
	}
	if Classify(0) != "trusted" || Classify(TrustENoSignature) != "unsigned" || Classify(TrustEBadDigest) != "bad_digest" {
		t.Fatal("classify")
	}
}
