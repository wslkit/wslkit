package disk

import (
	"errors"
	"strings"
	"testing"
)

const storeBase = `C:\Users\u\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState`

func TestPackageOfReadsBothSignals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		family   string
		base     string
		owned    bool
		mismatch bool
	}{
		{"store install", "CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc", storeBase, true, false},
		{"extended-length base path", "CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc", `\\?\` + storeBase, true, false},
		{"plain import", "", `C:\wsl\Ubuntu`, false, false},
		// Already moved out from under its package: still owned, and said so.
		{"moved out", "CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc", `D:\wsl\Ubuntu`, true, true},
		// In a package directory with the value gone: somebody edited it.
		{"edited registration", "", storeBase, true, true},
	} {
		p := PackageOf(Registration{Name: "Ubuntu", BasePath: tc.base, PackageFamilyName: tc.family})
		if p.Owned != tc.owned || (p.Mismatch != "") != tc.mismatch {
			t.Errorf("%s: %+v", tc.name, p)
		}
	}
}

// #91: move, relink, trash and rebuild break a distribution a package owns,
// and WSL itself does not refuse. The refusal has to be ErrRefused, so a
// script can tell "did not start" from "failed partway", and has to name the
// way out.
func TestGuardRefusesAPackagedDistribution(t *testing.T) {
	r := Registration{Name: "Ubuntu", BasePath: storeBase, PackageFamilyName: "CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc"}
	err := GuardPackaged(r, "move", false)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	for _, want := range []string{"CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc", "A move", "Settings > Apps", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}
	if err := GuardPackaged(r, "move", true); err != nil {
		t.Errorf("--force must let it through: %v", err)
	}
	if err := GuardPackaged(Registration{Name: "Ubuntu", BasePath: `C:\wsl\Ubuntu`}, "move", false); err != nil {
		t.Errorf("an ordinary import must not be refused: %v", err)
	}
}
