package disk

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

const family = "CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc"

func storeEnv(base string) *env.Env {
	e := env.New("t")
	vhd := env.Ok(env.VhdInfo{Path: base + `\ext4.vhdx`, FileSize: 1 << 30, VirtualSize: 1 << 40, MagicOK: true, HeaderOK: true}, "t")
	e.Distros = env.Ok([]env.Distro{{Name: "Ubuntu", Version: 2, BasePath: base, PackageFamilyName: family, Vhd: vhd}}, "t")
	return e
}

// #91: a Store distribution whose disk has been moved out of its package
// directory still runs, but its app's launcher, reset and uninstall may not
// find it. WSL's own --manage --move does not refuse this, so the doctor is
// where somebody already in that state hears about it.
func TestIntegrityWarnsAboutADiskMovedOutOfItsPackage(t *testing.T) {
	r := (Integrity{}).Run(storeEnv(`D:\wsl\Ubuntu`))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, family) || !strings.Contains(r.Summary, "outside the package") {
		t.Fatalf("moved out -> %s %q", r.Status, r.Summary)
	}
	inPlace := (Integrity{}).Run(storeEnv(`C:\Users\u\AppData\Local\Packages\` + family + `\LocalState`))
	if inPlace.Status != probe.OK {
		t.Fatalf("in its package directory -> %s %q", inPlace.Status, inPlace.Summary)
	}
}
