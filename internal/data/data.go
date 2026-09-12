// Package data embeds the compatibility matrix and other tables. The JSON files
// under /data are the source of truth and are refreshed by CI; this package only
// loads and validates them.
package data

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wslkit/wsldoctor/internal/wslver"
)

//go:embed all:files
var files embed.FS

type DistroCompat struct {
	Flavor       string   `json:"flavor"`
	OsVersion    string   `json:"os_version"`
	Format       string   `json:"format"`
	KnownBadMax  string   `json:"known_bad_max,omitempty"`
	KnownGoodMin string   `json:"known_good_min,omitempty"`
	MinRuntime   string   `json:"min_runtime,omitempty"` // set once bisected; overrides the known_* pair
	Symptom      string   `json:"symptom,omitempty"`
	Cause        string   `json:"cause,omitempty"`
	Refs         []string `json:"refs,omitempty"`
}

type Compat struct {
	Schema                  string            `json:"schema"`
	Updated                 string            `json:"updated"`
	LatestStable            string            `json:"latest_stable"`
	LatestPrerelease        string            `json:"latest_prerelease"`
	ModernFormatMin         string            `json:"modern_format_min"`
	ModernFormatRecommended string            `json:"modern_format_recommended"`
	Distros                 []DistroCompat    `json:"distros"`
	Refs                    map[string]string `json:"refs"`
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

// Validate checks every version string parses and the schema matches.
func (c *Compat) Validate() error {
	if c.Schema != "wsldoctor/compat/v1" {
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
		if d.MinRuntime == "" && d.KnownBadMax == "" && d.KnownGoodMin == "" {
			return fmt.Errorf("compat.json: distros[%d] has no version bound", i)
		}
	}
	return nil
}

// Lookup finds the row for a flavor/os_version pair (case-insensitive flavor,
// exact os_version, falling back to a major-version prefix match like "26.04" vs "26.04.1").
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
