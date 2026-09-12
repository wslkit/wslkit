// Package data embeds the compatibility matrix and other tables. The JSON files
// under files/ are the source of truth and are refreshed by CI; this package only
// loads and validates them.
package data

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wslkit/wslkit/internal/wslver"
)

//go:embed all:files
var files embed.FS

// Capability is something a runtime line gained at a given version and that a
// distro may require, e.g. "cgroup_v2": the runtime stopped mounting cgroup v1.
type Capability struct {
	Since       string   `json:"since"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Refs        []string `json:"refs,omitempty"`
}

type DistroCompat struct {
	Flavor       string   `json:"flavor"`
	OsVersion    string   `json:"os_version"`
	Format       string   `json:"format"`
	Requires     []string `json:"requires,omitempty"`    // capability names; see Compat.Capabilities
	MinRuntime   string   `json:"min_runtime,omitempty"` // explicit floor, combined with Requires
	KnownBadMax  string   `json:"known_bad_max,omitempty"`
	KnownGoodMin string   `json:"known_good_min,omitempty"`
	Symptom      string   `json:"symptom,omitempty"`
	Cause        string   `json:"cause,omitempty"`
	Refs         []string `json:"refs,omitempty"`
}

type Compat struct {
	Schema                  string                `json:"schema"`
	Updated                 string                `json:"updated"`
	LatestStable            string                `json:"latest_stable"`
	LatestPrerelease        string                `json:"latest_prerelease"`
	ModernFormatMin         string                `json:"modern_format_min"`
	ModernFormatRecommended string                `json:"modern_format_recommended"`
	Capabilities            map[string]Capability `json:"capabilities,omitempty"`
	Distros                 []DistroCompat        `json:"distros"`
	Refs                    map[string]string     `json:"refs"`
}

var (
	once    sync.Once
	compat  *Compat
	loadErr error
)

// LoadCompat returns the embedded matrix, parsed once.
func LoadCompat() (*Compat, error) {
	once.Do(func() {
		b, err := files.ReadFile("files/compat.json")
		if err != nil {
			loadErr = err
			return
		}
		var c Compat
		if err := json.Unmarshal(b, &c); err != nil {
			loadErr = fmt.Errorf("compat.json: %w", err)
			return
		}
		if err := c.Validate(); err != nil {
			loadErr = err
			return
		}
		compat = &c
	})
	return compat, loadErr
}

// Validate checks every version string parses, every required capability
// exists, and every row carries at least one version bound.
func (c *Compat) Validate() error {
	if c.Schema != "wslkit/compat/v1" {
		return fmt.Errorf("compat.json: unexpected schema %q", c.Schema)
	}
	for name, v := range map[string]string{
		"latest_stable": c.LatestStable, "latest_prerelease": c.LatestPrerelease,
		"modern_format_min": c.ModernFormatMin, "modern_format_recommended": c.ModernFormatRecommended,
	} {
		if _, err := wslver.Parse(v); err != nil {
			return fmt.Errorf("compat.json: %s: %w", name, err)
		}
	}
	for name, cap := range c.Capabilities {
		if cap.Title == "" {
			return fmt.Errorf("compat.json: capabilities.%s needs a title", name)
		}
		if _, err := wslver.Parse(cap.Since); err != nil {
			return fmt.Errorf("compat.json: capabilities.%s.since: %w", name, err)
		}
	}
	for i, d := range c.Distros {
		if d.Flavor == "" || d.OsVersion == "" {
			return fmt.Errorf("compat.json: distros[%d] needs flavor and os_version", i)
		}
		for name, v := range map[string]string{"known_bad_max": d.KnownBadMax, "known_good_min": d.KnownGoodMin, "min_runtime": d.MinRuntime} {
			if v == "" {
				continue
			}
			if _, err := wslver.Parse(v); err != nil {
				return fmt.Errorf("compat.json: distros[%d].%s: %w", i, name, err)
			}
		}
		for _, r := range d.Requires {
			if _, ok := c.Capabilities[r]; !ok {
				return fmt.Errorf("compat.json: distros[%d] requires unknown capability %q", i, r)
			}
		}
		if d.MinRuntime == "" && len(d.Requires) == 0 && d.KnownBadMax == "" && d.KnownGoodMin == "" {
			return fmt.Errorf("compat.json: distros[%d] has no version bound", i)
		}
	}
	return nil
}

// Lookup finds the row for a flavor/os_version pair (case-insensitive flavor,
// exact os_version, falling back to a prefix match like "26.04" vs "26.04.1").
func (c *Compat) Lookup(flavor, osVersion string) *DistroCompat {
	var prefix *DistroCompat
	for i := range c.Distros {
		d := &c.Distros[i]
		if !strings.EqualFold(d.Flavor, flavor) {
			continue
		}
		if d.OsVersion == osVersion {
			return d
		}
		if strings.HasPrefix(osVersion, d.OsVersion+".") && prefix == nil {
			prefix = d
		}
	}
	return prefix
}

// Requirement is one reason a distro needs a minimum runtime version.
type Requirement struct {
	Min        wslver.Version
	Capability string // "" for an explicit min_runtime
	Title      string
	Refs       []string
}

// MinimumRuntime combines a row's explicit min_runtime with the "since" version
// of every capability it requires. It returns the zero Version when the row
// carries no such bound (only known_bad/known_good observations). Requirements
// are sorted with the binding one first.
func (c *Compat) MinimumRuntime(d *DistroCompat) (wslver.Version, []Requirement) {
	var reqs []Requirement
	if d.MinRuntime != "" {
		reqs = append(reqs, Requirement{Min: wslver.MustParse(d.MinRuntime), Title: "explicit minimum for this distro release", Refs: d.Refs})
	}
	for _, name := range d.Requires {
		cap, ok := c.Capabilities[name]
		if !ok {
			continue // Validate rejects this; be lenient at runtime
		}
		reqs = append(reqs, Requirement{Min: wslver.MustParse(cap.Since), Capability: name, Title: cap.Title, Refs: cap.Refs})
	}
	if len(reqs) == 0 {
		return wslver.Version{}, nil
	}
	sort.SliceStable(reqs, func(i, j int) bool { return reqs[j].Min.Less(reqs[i].Min) })
	return reqs[0].Min, reqs
}
