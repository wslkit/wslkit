package data

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Cache is one catalogued path inside a distribution that commonly holds
// reclaimable data.
type Cache struct {
	// Path is absolute, or starts with ~/ to mean every real home
	// directory. It never ends in a slash.
	Path string `json:"path"`
	// Label names it in the table.
	Label string `json:"label"`
	// Safe reports whether emptying it loses only things that can be
	// fetched or rebuilt.
	//
	// False does not mean dangerous. It means wslkit cannot judge on the
	// user's behalf: a Docker storage directory is not a cache, and whether
	// its contents matter is not something a disk tool can know.
	Safe bool `json:"safe"`
	// Note is shown when the entry is large enough to matter.
	Note string `json:"note,omitempty"`
}

type cacheFile struct {
	Schema  string  `json:"schema"`
	Updated string  `json:"updated"`
	Caches  []Cache `json:"caches"`
}

var (
	cachesOnce sync.Once
	cachesVal  []Cache
	cachesErr  error
)

// Caches returns the cache catalogue. It is parsed once.
func Caches() ([]Cache, error) {
	cachesOnce.Do(func() {
		b, err := files.ReadFile("files/disk-caches.json")
		if err != nil {
			cachesErr = fmt.Errorf("data: reading the cache catalogue: %w", err)
			return
		}
		var f cacheFile
		if err := json.Unmarshal(b, &f); err != nil {
			cachesErr = fmt.Errorf("data: parsing the cache catalogue: %w", err)
			return
		}
		if err := validateCaches(f.Caches); err != nil {
			cachesErr = err
			return
		}
		cachesVal = f.Caches
	})
	return cachesVal, cachesErr
}

// validateCaches enforces the shape the usage report depends on.
func validateCaches(list []Cache) error {
	if len(list) == 0 {
		return fmt.Errorf("data: the cache catalogue has no entries")
	}
	seen := map[string]bool{}
	for i, c := range list {
		n := i + 1
		if c.Path == "" {
			return fmt.Errorf("data: cache entry %d has no path", n)
		}
		if c.Label == "" {
			return fmt.Errorf("data: cache entry %d (%s) has no label", n, c.Path)
		}
		if !strings.HasPrefix(c.Path, "/") && !strings.HasPrefix(c.Path, "~/") {
			return fmt.Errorf("data: cache entry %d has a relative path %q; it must be absolute or start with ~/", n, c.Path)
		}
		if strings.HasSuffix(c.Path, "/") {
			// The containment check that stops /var/log claiming
			// /var/logbook requires a separator after the prefix, which
			// a trailing slash would already have consumed.
			return fmt.Errorf("data: cache entry %d ends with a slash: %q", n, c.Path)
		}
		if seen[c.Path] {
			return fmt.Errorf("data: cache entry %d repeats the path %q", n, c.Path)
		}
		seen[c.Path] = true
	}
	return nil
}
