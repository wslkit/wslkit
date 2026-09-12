package data

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wslkit/wslkit/internal/wslver"
)

// ConfigKey describes one accepted .wslconfig key.
type ConfigKey struct {
	Section         string `json:"section"`
	Key             string `json:"key"`
	Type            string `json:"type,omitempty"` // bool, int, size, string, path, enum:a,b,c
	Default         string `json:"default,omitempty"`
	Documented      bool   `json:"documented"`
	InSource        bool   `json:"in_source"`
	MinWindowsBuild int    `json:"min_windows_build,omitempty"`
	MinWSL          string `json:"min_wsl,omitempty"`
	DeprecatedIn    string `json:"deprecated_in,omitempty"`
	AliasOf         string `json:"alias_of,omitempty"`
	Note            string `json:"note,omitempty"`
}

// EnumValues returns the allowed values for an enum-typed key, or nil.
func (k ConfigKey) EnumValues() []string {
	if !strings.HasPrefix(k.Type, "enum:") {
		return nil
	}
	return strings.Split(strings.TrimPrefix(k.Type, "enum:"), ",")
}

type ConfigKeys struct {
	Schema       string      `json:"schema"`
	Updated      string      `json:"updated"`
	SourceCommit string      `json:"source_commit"`
	Keys         []ConfigKey `json:"keys"`
}

var (
	keysOnce sync.Once
	keysTbl  *ConfigKeys
	keysErr  error
)

// LoadConfigKeys returns the embedded .wslconfig key table, parsed once.
func LoadConfigKeys() (*ConfigKeys, error) {
	keysOnce.Do(func() {
		b, err := files.ReadFile("files/wslconfig-keys.json")
		if err != nil {
			keysErr = err
			return
		}
		var t ConfigKeys
		if err := json.Unmarshal(b, &t); err != nil {
			keysErr = fmt.Errorf("wslconfig-keys.json: %w", err)
			return
		}
		if err := t.Validate(); err != nil {
			keysErr = err
			return
		}
		keysTbl = &t
	})
	return keysTbl, keysErr
}

func (t *ConfigKeys) Validate() error {
	if t.Schema != "wslkit/wslconfig-keys/v1" {
		return fmt.Errorf("wslconfig-keys.json: unexpected schema %q", t.Schema)
	}
	seen := map[string]bool{}
	for i, k := range t.Keys {
		id := strings.ToLower(k.Section + "." + k.Key)
		if k.Section == "" || k.Key == "" || seen[id] {
			return fmt.Errorf("wslconfig-keys.json: keys[%d] invalid or duplicate (%s)", i, id)
		}
		seen[id] = true
		if k.MinWSL != "" {
			if _, err := wslver.Parse(k.MinWSL); err != nil {
				return fmt.Errorf("wslconfig-keys.json: %s min_wsl: %w", id, err)
			}
		}
		if k.AliasOf != "" && !strings.Contains(k.AliasOf, ".") {
			return fmt.Errorf("wslconfig-keys.json: %s alias_of must be section.key", id)
		}
	}
	for _, k := range t.Keys {
		if k.AliasOf != "" && !seen[strings.ToLower(k.AliasOf)] {
			return fmt.Errorf("wslconfig-keys.json: %s.%s alias_of %q is not a known key", k.Section, k.Key, k.AliasOf)
		}
	}
	return nil
}

// Lookup finds a key case-insensitively. ok is false for unknown keys.
func (t *ConfigKeys) Lookup(section, key string) (ConfigKey, bool) {
	for _, k := range t.Keys {
		if strings.EqualFold(k.Section, section) && strings.EqualFold(k.Key, key) {
			return k, true
		}
	}
	return ConfigKey{}, false
}

// SectionsFor returns the sections in which a key name exists (case-insensitive).
func (t *ConfigKeys) SectionsFor(key string) []string {
	var out []string
	for _, k := range t.Keys {
		if strings.EqualFold(k.Key, key) {
			out = append(out, k.Section)
		}
	}
	return out
}
