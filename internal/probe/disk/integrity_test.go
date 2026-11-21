package disk

import (
	"errors"
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

func TestIntegrityNamesDeniedReadAccessRatherThanElevation(t *testing.T) {
	// #136: a refused open can be an ACL deny rather than a missing privilege.
	// The collector keeps UserReadable on that Field, so DSK002 must say so and
	// point at DSK006 instead of telling the user to elevate.
	denied := false
	vhd := env.Fail[env.VhdInfo](env.ErrNeedsElevation, "t", errors.New("Access is denied."))
	vhd.Value = env.VhdInfo{Path: `C:\wsl\ext4.vhdx`, UserReadable: &denied}
	e := env.New("t")
	e.Distros = env.Ok([]env.Distro{{Name: "Ubuntu", Version: 2, Vhd: vhd}}, "t")

	r := (Integrity{}).Run(e)
	if !strings.Contains(r.Detail, "cannot read the VHDX (its access list, not elevation)") || !strings.Contains(r.Detail, "DSK006") {
		t.Fatalf("denied read -> %q", r.Detail)
	}
	if strings.Contains(r.Detail, "not readable without elevation") {
		t.Fatalf("still suggests elevation for an ACL deny: %q", r.Detail)
	}

	// Without the read check the conservative elevation wording stays.
	unknown := env.Fail[env.VhdInfo](env.ErrNeedsElevation, "t", errors.New("Access is denied."))
	e.Distros = env.Ok([]env.Distro{{Name: "Ubuntu", Version: 2, Vhd: unknown}}, "t")
	r = (Integrity{}).Run(e)
	if !strings.Contains(r.Detail, "not readable without elevation") {
		t.Fatalf("unknown read check -> %q", r.Detail)
	}
}
