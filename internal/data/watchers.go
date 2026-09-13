package data

import (
	"encoding/json"
	"fmt"
	"sync"
)

// A file watcher does not work across the Windows filesystem. inotify is a
// kernel facility over a real Linux filesystem; /mnt/c is a protocol to a
// server on the Windows side that never says a file changed. A watch set there
// is accepted, and then never fires.
//
// This table is the tools that depend on noticing a change, and the one setting
// that makes each of them poll instead.

// Watcher is one such tool.
type Watcher struct {
	// Match is the program name, compared against the process command name
	// and against the base name of its first argument.
	Match string `json:"match"`
	// Knob is what switches that tool to polling. Polling is slower and
	// busier, which is why nothing does it by default, but it is the
	// difference between a watch that works and one that does not.
	Knob string `json:"knob"`
	// Note is a caveat worth repeating at the point of use.
	Note string `json:"note,omitempty"`
}

// Watchers is the whole table.
type Watchers struct {
	Schema  string    `json:"schema"`
	Updated string    `json:"updated"`
	Tools   []Watcher `json:"tools"`
}

var (
	watchOnce sync.Once
	watchTbl  *Watchers
	watchErr  error
)

// LoadWatchers returns the embedded watcher table, parsed once.
func LoadWatchers() (*Watchers, error) {
	watchOnce.Do(func() {
		b, err := files.ReadFile("files/watchers.json")
		if err != nil {
			watchErr = fmt.Errorf("data: reading the watcher table: %w", err)
			return
		}
		var t Watchers
		if err := json.Unmarshal(b, &t); err != nil {
			watchErr = fmt.Errorf("data: parsing the watcher table: %w", err)
			return
		}
		if err := t.Validate(); err != nil {
			watchErr = err
			return
		}
		watchTbl = &t
	})
	return watchTbl, watchErr
}

// Validate enforces the shape the probe depends on.
func (t *Watchers) Validate() error {
	if len(t.Tools) == 0 {
		return fmt.Errorf("data: the watcher table is empty")
	}
	seen := map[string]bool{}
	for i, w := range t.Tools {
		if w.Match == "" || w.Knob == "" {
			return fmt.Errorf("data: watcher %d has no match or no knob", i+1)
		}
		if seen[w.Match] {
			return fmt.Errorf("data: watcher %q appears twice", w.Match)
		}
		seen[w.Match] = true
	}
	return nil
}

// LookupWatcher finds one tool by the name a process runs under. The match is
// exact and case-sensitive: these are program names, not words.
func (t *Watchers) LookupWatcher(name string) (Watcher, bool) {
	for _, w := range t.Tools {
		if w.Match == name {
			return w, true
		}
	}
	return Watcher{}, false
}
