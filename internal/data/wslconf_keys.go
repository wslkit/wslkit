package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// /etc/wsl.conf is the per-distribution file, not to be confused with
// .wslconfig, which is the Windows-side file for the VM as a whole. They have
// different keys, different sections and different owners, and conflating them
// is a common enough mistake that the two tables are kept deliberately apart.

// WslConfKey describes one accepted /etc/wsl.conf key.
type WslConfKey struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	// Type is bool, string, path, int, or enum:a,b,c.
	Type    string `json:"type,omitempty"`
	Default string `json:"default,omitempty"`
	// Desc is one sentence on what it does, shown when a value is wrong.
	Desc string `json:"desc,omitempty"`
	// MinWSL is the runtime below which the key is accepted but ignored,
	// which is worse than being rejected: the setting silently does nothing.
	MinWSL string `json:"min_wsl,omitempty"`
	// Documented records whether Microsoft publishes it. An undocumented key
	// works but may change without notice.
	Documented bool `json:"documented"`
	// Note is a caveat worth repeating at the point of use.
	Note string `json:"note,omitempty"`
}

// EnumValues returns the allowed values for an enum-typed key, or nil.
func (k WslConfKey) EnumValues() []string {
	if !strings.HasPrefix(k.Type, "enum:") {
		return nil
	}
	return strings.Split(strings.TrimPrefix(k.Type, "enum:"), ",")
}

// WslConfKeys is the whole table.
type WslConfKeys struct {
	Schema       string       `json:"schema"`
	Updated      string       `json:"updated"`
	SourceCommit string       `json:"source_commit"`
	Keys         []WslConfKey `json:"keys"`
}

var (
	confOnce sync.Once
	confTbl  *WslConfKeys
	confErr  error
)

// LoadWslConfKeys returns the embedded wsl.conf key table, parsed once.
func LoadWslConfKeys() (*WslConfKeys, error) {
	confOnce.Do(func() {
		b, err := files.ReadFile("files/wslconf-keys.json")
		if err != nil {
			confErr = fmt.Errorf("data: reading the wsl.conf key table: %w", err)
			return
		}
		var t WslConfKeys
		if err := json.Unmarshal(b, &t); err != nil {
			confErr = fmt.Errorf("data: parsing the wsl.conf key table: %w", err)
			return
		}
		if err := t.Validate(); err != nil {
			confErr = err
			return
		}
		confTbl = &t
	})
	return confTbl, confErr
}

// Validate enforces the shape the lint depends on.
func (t *WslConfKeys) Validate() error {
	if len(t.Keys) == 0 {
		return fmt.Errorf("data: the wsl.conf key table is empty")
	}
	seen := map[string]bool{}
	for i, k := range t.Keys {
		n := i + 1
		if k.Section == "" || k.Key == "" {
			return fmt.Errorf("data: wsl.conf key %d has no section or no key", n)
		}
		// Sections and keys are stored as WSL spells them, because that is
		// what gets suggested back to the user, and matched case-insensitively
		// because that is how WSL reads the file. One of them really is
		// camel case: fileServer.
		id := strings.ToLower(k.Section) + "." + strings.ToLower(k.Key)
		if seen[id] {
			return fmt.Errorf("data: wsl.conf key %q appears twice", id)
		}
		seen[id] = true
	}
	return nil
}

// Lookup finds one key. Section and key are matched case-insensitively,
// because that is how WSL reads the file.
func (t *WslConfKeys) Lookup(section, key string) (WslConfKey, bool) {
	for _, k := range t.Keys {
		if strings.EqualFold(k.Section, section) && strings.EqualFold(k.Key, key) {
			return k, true
		}
	}
	return WslConfKey{}, false
}

// KnownSection reports whether any key lives in this section. Telling a bad
// section from a bad key is worth the distinction: one means the whole block
// is ignored, the other means one line is.
func (t *WslConfKeys) KnownSection(section string) bool {
	for _, k := range t.Keys {
		if strings.EqualFold(k.Section, section) {
			return true
		}
	}
	return false
}

// Sections lists every section, in a stable order.
func (t *WslConfKeys) Sections() []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range t.Keys {
		if !seen[k.Section] {
			seen[k.Section] = true
			out = append(out, k.Section)
		}
	}
	sort.Strings(out)
	return out
}

// KeysIn lists the key names in one section, in a stable order.
func (t *WslConfKeys) KeysIn(section string) []string {
	var out []string
	for _, k := range t.Keys {
		if strings.EqualFold(k.Section, section) {
			out = append(out, k.Key)
		}
	}
	sort.Strings(out)
	return out
}
