package disk

import (
	"fmt"
	"strings"
)

// A distribution installed from the Store, or from any appx, belongs to that
// package as well as to WSL: its Lxss key carries the package's family name,
// and its disk sits in the package's LocalState directory. The launcher
// (ubuntu.exe and friends) looks the distribution up by name, and the package
// expects its disk where it put it.
//
// WSL does not guard on this. MoveDistribution in LxssUserSession.cpp checks
// that the distribution is stopped and nothing else, so wsl --manage --move has
// the same hole as any tool that rehomes a disk (#91).

// packagesDir is the part of a package directory's path that does not depend
// on the user: %LOCALAPPDATA%\Packages\<family>\LocalState. A substring rather
// than the expanded variable keeps this pure, and it also matches a disk whose
// registration names another user's profile.
const packagesDir = `\appdata\local\packages\`

// Packaged says whether a Windows package owns a distribution, and how sure
// that is.
type Packaged struct {
	// Owned is true when either signal says so.
	Owned bool
	// Family is the PackageFamilyName value, when there is one.
	Family string
	// Mismatch is set when only one of the two signals agrees: a family name
	// with the disk outside the package directory means the disk has already
	// been moved out from under the package, and a disk in a package
	// directory with no family name is a registration somebody has edited.
	Mismatch string
}

// PackageOf reads the two signals. Trusting either alone would miss the
// broken states the guard exists to stop people making worse.
func PackageOf(r Registration) Packaged {
	family := strings.TrimSpace(r.PackageFamilyName)
	inPackageDir := strings.Contains(strings.ToLower(r.Base()), packagesDir)
	p := Packaged{Owned: family != "" || inPackageDir, Family: family}
	switch {
	case family != "" && !inPackageDir:
		p.Mismatch = fmt.Sprintf("its registration names the package %s, but its disk is not in that package's directory: it may already have been moved out from under the package", family)
	case family == "" && inPackageDir:
		p.Mismatch = "its disk is in a Windows package directory, but its registration names no package"
	}
	return p
}

// GuardPackaged refuses an operation on a distribution a Windows package
// owns, unless forced. what is the operation, said the way a person would:
// "move", "rename".
func GuardPackaged(r Registration, what string, force bool) error {
	p := PackageOf(r)
	if !p.Owned || force {
		return nil
	}
	owner := "a Windows package"
	if p.Family != "" {
		owner = "the Windows package " + p.Family
	}
	msg := fmt.Sprintf("%s was installed by %s, which expects to find it by name with its disk in the package's own directory. A %s would leave the app launching into nothing. Remove it through Settings > Apps > Installed apps, or pass --force if you know the package no longer needs it",
		r.Name, owner, what)
	if p.Mismatch != "" {
		msg += " (" + p.Mismatch + ")"
	}
	return fmt.Errorf("%w: %s", ErrRefused, msg)
}
