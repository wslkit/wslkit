package cli

import (
	"errors"
	"time"
)

// topMode is how one invocation of top runs.
type topMode int

const (
	// topReport measures over one interval, prints one report and exits.
	topReport topMode = iota
	// topWatch redraws every interval until interrupted.
	topWatch
)

// chooseTopMode decides between watching and printing once.
//
// On a console top refreshes, as top, htop and docker stats do. Anywhere else,
// a pipe, a file, or --json, it prints one report and exits, so a script never
// waits on a command that does not end; docker stats streams when piped, and
// grew --no-stream to undo it. --once and --watch say which regardless.
//
// An interval of zero is a single instant sample with no rates, which cannot
// be watched: there is nothing to redraw it against.
func chooseTopMode(once, watch, jsonOut, console bool, interval time.Duration) (topMode, error) {
	switch {
	case once && watch:
		return 0, errors.New("--watch and --once ask for opposite things")
	case watch && interval == 0:
		return 0, errors.New("--watch needs an --interval above zero")
	case watch:
		return topWatch, nil
	case once, jsonOut, !console, interval == 0:
		return topReport, nil
	default:
		return topWatch, nil
	}
}
