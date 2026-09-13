// Package disk implements `wslkit disk`: inspecting and maintaining the .vhdx
// files behind WSL 2 distributions.
//
// The package splits the way the rest of wslkit does (ADR 0004). This file is
// pure: types, name resolution, path derivation and precondition rules, all
// functions of their arguments, compiled and tested on Linux. Everything that
// touches the registry, the filesystem, virtdisk.dll or wsl.exe lives in the
// _windows files behind an interface, so the command logic can be exercised
// against a fake.
package disk

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultVhdName is the file WSL creates when a registration carries no
// explicit VhdFileName.
const DefaultVhdName = "ext4.vhdx"

// Registration mirrors one HKCU\...\Lxss\{guid} key. Field names match the
// registry value names so the trash manifest round-trips without a mapping
// table.
type Registration struct {
	GUID                string `json:"guid"`
	Name                string `json:"distribution_name"`
	IsDefault           bool   `json:"is_default"`
	Version             int    `json:"version"`
	State               int    `json:"state"`
	BasePath            string `json:"base_path"`
	VhdFileName         string `json:"vhd_file_name,omitempty"`
	Flags               int    `json:"flags"`
	DefaultUID          int    `json:"default_uid"`
	RunOOBE             int    `json:"run_oobe"`
	Modern              int    `json:"modern"`
	Flavor              string `json:"flavor,omitempty"`
	OsVersion           string `json:"os_version,omitempty"`
	ShortcutPath        string `json:"shortcut_path,omitempty"`
	TerminalProfilePath string `json:"terminal_profile_path,omitempty"`
}

// Lxss distribution states, as written by the WSL service.
const (
	StateNormal       = 1
	StateInstalling   = 3
	StateUninstalling = 4
)

// Base returns BasePath with the extended-length prefix stripped, which is how
// WSL stores it but not a form every Win32 call accepts.
func (r Registration) Base() string {
	return strings.TrimPrefix(r.BasePath, `\\?\`)
}

// VhdName is the VHD file name, defaulting the way WSL does.
func (r Registration) VhdName() string {
	if r.VhdFileName == "" {
		return DefaultVhdName
	}
	return r.VhdFileName
}

// VhdPath is the full path to the distribution disk. It is empty for a WSL 1
// distribution, which has no disk image, or when the registration carries no
// base path.
func (r Registration) VhdPath() string {
	if r.Version != 2 || r.BasePath == "" {
		return ""
	}
	return filepath.Join(r.Base(), r.VhdName())
}

// Errors that command code matches on rather than comparing strings.
var (
	// ErrNoDistros means the Lxss key is absent or empty: WSL has no
	// distributions registered for this user.
	ErrNoDistros = errors.New("disk: no WSL distributions are registered for this user")
	// ErrNotFound means the requested name matched nothing.
	ErrNotFound = errors.New("disk: no such distribution")
	// ErrAmbiguous means a case-insensitive name matched more than one
	// registration, which WSL permits but wslkit refuses to guess at.
	ErrAmbiguous = errors.New("disk: the name matches more than one distribution")
	// ErrNoDefault means no name was given and no default is set.
	ErrNoDefault = errors.New("disk: no distribution named and no default is set")
	// ErrNotWSL2 means the distribution has no virtual disk to operate on.
	ErrNotWSL2 = errors.New("disk: the distribution is WSL 1 and has no virtual disk")
	// ErrRunning means the distribution must be stopped first.
	ErrRunning = errors.New("disk: the distribution is running")
	// ErrNotVHDX means the disk is not a .vhdx and wslkit will not touch it.
	ErrNotVHDX = errors.New("disk: the distribution disk is not a .vhdx")
)

// Resolve picks one registration by name. An empty name selects the default
// distribution. Matching is case-insensitive, because that is how wsl.exe
// matches, but an exact match always wins over a case-insensitive one so a user
// with distributions differing only in case can still address them.
func Resolve(list []Registration, name string) (Registration, error) {
	if len(list) == 0 {
		return Registration{}, ErrNoDistros
	}
	if name == "" {
		for _, r := range list {
			if r.IsDefault {
				return r, nil
			}
		}
		return Registration{}, ErrNoDefault
	}
	for _, r := range list {
		if r.Name == name {
			return r, nil
		}
	}
	var hits []Registration
	for _, r := range list {
		if strings.EqualFold(r.Name, name) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return Registration{}, fmt.Errorf("%w: %q (known: %s)", ErrNotFound, name, strings.Join(Names(list), ", "))
	case 1:
		return hits[0], nil
	default:
		return Registration{}, fmt.Errorf("%w: %q", ErrAmbiguous, name)
	}
}

// Names lists distribution names in display order: the default first, then the
// rest alphabetically, which is the order `disk list` prints.
func Names(list []Registration) []string {
	sorted := Sorted(list)
	out := make([]string, 0, len(sorted))
	for _, r := range sorted {
		out = append(out, r.Name)
	}
	return out
}

// Sorted returns the registrations in display order without mutating the input.
func Sorted(list []Registration) []Registration {
	out := append([]Registration(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Precondition is one requirement a mutating command places on a distribution,
// evaluated before anything is changed so the whole set can be reported at once
// rather than failing one at a time.
type Precondition struct {
	// Name is a stable identifier, used in --json and in tests.
	Name string `json:"name"`
	// OK is whether the requirement is met.
	OK bool `json:"ok"`
	// Detail explains a failure in the user's terms, including what to do.
	Detail string `json:"detail,omitempty"`
}

// State is what the precondition rules are evaluated against: the registration
// plus the few facts that need a Windows call to establish.
type State struct {
	Reg Registration
	// Running is whether the distribution is currently up. Determining this
	// must not itself start the VM.
	Running bool
	// VhdExists is whether the disk file is present on disk.
	VhdExists bool
	// VhdInUse is whether the disk file could not be opened exclusively,
	// which catches a disk held by something other than this distribution.
	VhdInUse bool
}

// Check evaluates the common preconditions for a mutating disk command. It
// reports every failure, not just the first, so a user fixes one thing and
// succeeds rather than discovering the next obstacle on the next run.
func Check(s State) []Precondition {
	var out []Precondition

	out = append(out, Precondition{
		Name:   "wsl2",
		OK:     s.Reg.Version == 2,
		Detail: detailIf(s.Reg.Version != 2, "the distribution is WSL 1, which stores files directly on NTFS and has no virtual disk; convert it with wsl --set-version"),
	})

	out = append(out, Precondition{
		Name:   "state_normal",
		OK:     s.Reg.State == StateNormal,
		Detail: detailIf(s.Reg.State != StateNormal, fmt.Sprintf("the distribution is in state %d, not %d (normal); an install or uninstall may be in progress", s.Reg.State, StateNormal)),
	})

	path := s.Reg.VhdPath()
	isVHDX := strings.EqualFold(filepath.Ext(path), ".vhdx")
	out = append(out, Precondition{
		Name:   "vhdx",
		OK:     path != "" && isVHDX,
		Detail: detailIf(path == "" || !isVHDX, "the distribution disk is not a .vhdx; wslkit does not modify other disk formats"),
	})

	out = append(out, Precondition{
		Name:   "vhd_exists",
		OK:     s.VhdExists,
		Detail: detailIf(!s.VhdExists, "the disk file recorded in the registry is missing; wslkit disk orphans finds disks whose registration is gone, and relink repairs a registration that points at the wrong path"),
	})

	out = append(out, Precondition{
		Name:   "stopped",
		OK:     !s.Running,
		Detail: detailIf(s.Running, "the distribution is running and the host compute service holds its disk open; stop it with wsl --terminate, or pass --shutdown to let wslkit do it"),
	})

	out = append(out, Precondition{
		Name:   "disk_free",
		OK:     !s.VhdInUse,
		Detail: detailIf(s.VhdInUse, "the disk file is open in another process; close anything that has it mounted, including a previous wslkit run"),
	})

	return out
}

func detailIf(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// Unmet returns only the failing preconditions.
func Unmet(ps []Precondition) []Precondition {
	var out []Precondition
	for _, p := range ps {
		if !p.OK {
			out = append(out, p)
		}
	}
	return out
}

// FirstError turns unmet preconditions into one error suitable for exiting on,
// preferring the sentinel errors so callers can match on cause.
func FirstError(ps []Precondition) error {
	unmet := Unmet(ps)
	if len(unmet) == 0 {
		return nil
	}
	base := map[string]error{
		"wsl2":      ErrNotWSL2,
		"stopped":   ErrRunning,
		"vhdx":      ErrNotVHDX,
		"disk_free": ErrRunning,
	}
	p := unmet[0]
	if e, ok := base[p.Name]; ok {
		return fmt.Errorf("%w: %s", e, p.Detail)
	}
	return fmt.Errorf("disk: %s: %s", p.Name, p.Detail)
}
