//go:build windows

package disk

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// LxssKey is where WSL keeps its per-user registrations.
const LxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`

// RegistryKeyFor is the full key path of one distribution, as printed by
// `disk info` so a user can find it in the registry editor.
func RegistryKeyFor(guid string) string { return LxssKey + `\` + guid }

// WindowsRegistry is the production Registry, reading HKCU.
type WindowsRegistry struct {
	// Root lets tests point the whole package at a scratch key instead of
	// the user's real WSL registration.
	Root registry.Key
	// Path overrides LxssKey, again for tests.
	Path string
}

// NewRegistry returns the production registry accessor.
func NewRegistry() *WindowsRegistry {
	return &WindowsRegistry{Root: registry.CURRENT_USER, Path: LxssKey}
}

func (r *WindowsRegistry) root() registry.Key {
	if r.Root == 0 {
		return registry.CURRENT_USER
	}
	return r.Root
}

func (r *WindowsRegistry) path() string {
	if r.Path == "" {
		return LxssKey
	}
	return r.Path
}

// Distros enumerates every registration.
//
// A key that cannot be used produces a warning and is skipped: one unusable
// entry must not stop the command reporting every distribution that is fine.
func (r *WindowsRegistry) Distros() ([]Registration, []string, error) {
	k, err := registry.OpenKey(r.root(), r.path(), registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			// WSL has never registered anything for this user. That is
			// an empty list, not a failure.
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("disk: opening %s: %w", r.path(), err)
	}
	defer k.Close()

	defaultGUID, _, _ := k.GetStringValue("DefaultDistribution")
	guids, err := k.ReadSubKeyNames(0)
	if err != nil {
		return nil, nil, fmt.Errorf("disk: listing the distributions under %s: %w", r.path(), err)
	}

	var out []Registration
	var warnings []string
	for _, guid := range guids {
		if !strings.HasPrefix(guid, "{") {
			continue
		}
		dk, err := registry.OpenKey(k, guid, registry.READ)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("the registration %s could not be opened and was skipped: %v", guid, err))
			continue
		}
		reg, warn := readRegistration(dk, guid, defaultGUID)
		dk.Close()
		if warn != "" {
			warnings = append(warnings, warn)
			continue
		}
		out = append(out, reg)
	}
	return out, warnings, nil
}

func readRegistration(dk registry.Key, guid, defaultGUID string) (Registration, string) {
	r := Registration{GUID: guid, IsDefault: strings.EqualFold(guid, defaultGUID)}
	r.Name, _, _ = dk.GetStringValue("DistributionName")
	r.BasePath, _, _ = dk.GetStringValue("BasePath")
	r.VhdFileName, _, _ = dk.GetStringValue("VhdFileName")
	r.Flavor, _, _ = dk.GetStringValue("Flavor")
	r.OsVersion, _, _ = dk.GetStringValue("OsVersion")
	r.ShortcutPath, _, _ = dk.GetStringValue("ShortcutPath")
	r.TerminalProfilePath, _, _ = dk.GetStringValue("TerminalProfilePath")

	// Version defaults to 2: a registration written before the value
	// existed is a WSL 2 distribution.
	r.Version = intOr(dk, "Version", 2)
	r.State = intOr(dk, "State", 0)
	r.Flags = intOr(dk, "Flags", 0)
	r.DefaultUID = intOr(dk, "DefaultUid", 0)
	r.RunOOBE = intOr(dk, "RunOOBE", 0)
	r.Modern = intOr(dk, "Modern", 0)

	// An empty value counts as absent. Both of these are fatal to the entry
	// rather than to the command.
	if r.Name == "" {
		return r, fmt.Sprintf("the registration %s has no DistributionName and was skipped", guid)
	}
	if r.BasePath == "" {
		// Without a base path the disk name would resolve against the
		// working directory, which is not a real location.
		return r, fmt.Sprintf("the distribution %s has no BasePath and was skipped", r.Name)
	}
	return r, ""
}

func intOr(k registry.Key, name string, fallback int) int {
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return fallback
	}
	return int(v)
}

// ReadString reads one value from a distribution key. Absent is reported as
// present=false with no error.
func (r *WindowsRegistry) ReadString(guid, name string) (string, bool, error) {
	k, err := registry.OpenKey(r.root(), RegistryKeyFor(guid), registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("disk: opening the registration %s: %w", guid, err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("disk: reading %s from %s: %w", name, guid, err)
	}
	return v, true, nil
}

// WriteString writes one value to a distribution key.
func (r *WindowsRegistry) WriteString(guid, name, value string) error {
	k, err := registry.OpenKey(r.root(), RegistryKeyFor(guid), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("disk: opening the registration %s for writing: %w", guid, err)
	}
	defer k.Close()
	if err := k.SetStringValue(name, value); err != nil {
		return fmt.Errorf("disk: writing %s to %s: %w", name, guid, err)
	}
	return nil
}

// ReadDWORD reads one numeric value from a distribution key.
func (r *WindowsRegistry) ReadDWORD(guid, name string) (uint32, bool, error) {
	k, err := registry.OpenKey(r.root(), RegistryKeyFor(guid), registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("disk: opening the registration %s: %w", guid, err)
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("disk: reading %s from %s: %w", name, guid, err)
	}
	return uint32(v), true, nil
}

// WriteDWORD writes one numeric value to a distribution key.
func (r *WindowsRegistry) WriteDWORD(guid, name string, value uint32) error {
	k, err := registry.OpenKey(r.root(), RegistryKeyFor(guid), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("disk: opening the registration %s for writing: %w", guid, err)
	}
	defer k.Close()
	if err := k.SetDWordValue(name, value); err != nil {
		return fmt.Errorf("disk: writing %s to %s: %w", name, guid, err)
	}
	return nil
}

// DefaultDistribution reads the GUID of the default distribution.
//
// It lives on the Lxss key itself, not on any distribution, so a restored
// distribution has to have it put back separately from its own values.
func (r *WindowsRegistry) DefaultDistribution() (string, bool, error) {
	k, err := registry.OpenKey(r.root(), r.path(), registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("disk: opening %s: %w", r.path(), err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue("DefaultDistribution")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("disk: reading the default distribution: %w", err)
	}
	return v, true, nil
}

// SetDefaultDistribution makes one distribution the default.
func (r *WindowsRegistry) SetDefaultDistribution(guid string) error {
	k, err := registry.OpenKey(r.root(), r.path(), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("disk: opening %s for writing: %w", r.path(), err)
	}
	defer k.Close()
	if err := k.SetStringValue("DefaultDistribution", guid); err != nil {
		return fmt.Errorf("disk: writing the default distribution: %w", err)
	}
	return nil
}
