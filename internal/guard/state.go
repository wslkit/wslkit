package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Whether a distribution was running before the machine slept is the single
// fact that separates "WSL is wedged" from "WSL is starting from cold, slowly".
// Without it, an unattended recovery either shuts down a machine that was
// merely booting or refuses to act at all.
//
// Windows offers a suspend notification that would record it at exactly the
// right moment, and using it means a resident process that has to survive the
// suspend it is watching for. This takes the cheaper route: every run records
// what was running, and the next one reads what the last one saw. On a machine
// where the recovery runs at logon and on every resume, the record is at most
// one sleep old, which is the only thing it needs to be right about.

// State is what the last run saw.
type State struct {
	// Running is the set of distributions that were up.
	Running []string `json:"running"`
	// At is when it was recorded.
	At time.Time `json:"at"`
}

// LoadState reads the record. A missing or unreadable file is reported as "no
// record" rather than as an error: the first run on a machine has none, and
// that case decides not to act rather than failing.
func LoadState(path string) (State, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, false
	}
	if s.At.IsZero() {
		return State{}, false
	}
	return s, true
}

// SaveState records what is running now.
func SaveState(path string, running []string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(State{Running: running, At: now}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// Stale reports whether a record is too old to reason from.
//
// A record from last week is not evidence about this morning: the machine has
// been up and down since, and acting on it would be acting on a guess wearing
// the clothes of a measurement.
func (s State) Stale(now time.Time, max time.Duration) bool {
	age := now.Sub(s.At)
	return age < 0 || age > max
}

// MaxStateAge is how long a record stays worth believing. A day covers a
// weekend of suspends; past that, whatever was running has long since stopped
// being the question.
const MaxStateAge = 24 * time.Hour
