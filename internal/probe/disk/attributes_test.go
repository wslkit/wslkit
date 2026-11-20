package disk

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func attrEnv(v env.VhdInfo) *env.Env {
	e := env.New("t")
	e.Distros = env.Ok([]env.Distro{{Name: "u", Version: 2, Vhd: env.Ok(v, "t")}}, "t")
	return e
}

// Measured on WSL 3.0.5: an NTFS-compressed disk attaches, an EFS-encrypted
// one fails with MountDisk/HCS/E_ACCESSDENIED.
func TestAttributes(t *testing.T) {
	if r := (Attributes{}).Run(attrEnv(env.VhdInfo{Compressed: true})); r.Status != probe.OK || !strings.Contains(r.Detail, "compressed") {
		t.Errorf("compressed -> %s %q %q", r.Status, r.Summary, r.Detail)
	}
	r := (Attributes{}).Run(attrEnv(env.VhdInfo{Encrypted: true}))
	if r.Status != probe.Fail || !strings.Contains(r.Detail, "HCS/E_ACCESSDENIED") || !strings.Contains(r.FixHint, "cipher /d") {
		t.Errorf("encrypted -> %s %q", r.Status, r.Summary)
	}
	if r := (Attributes{}).Run(attrEnv(env.VhdInfo{})); r.Status != probe.OK {
		t.Errorf("plain -> %s %q", r.Status, r.Summary)
	}
}
