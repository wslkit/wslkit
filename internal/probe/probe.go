// Package probe defines the Probe interface, Result, ranking and dependency
// cascade. Probes are pure functions of *env.Env.
package probe

import (
	"fmt"
	"sort"

	"github.com/wslkit/wslkit/internal/env"
)

type Status string

const (
	OK      Status = "OK"
	Warn    Status = "WARN"
	Fail    Status = "FAIL"
	Unknown Status = "UNKNOWN" // could not determine (needs elevation, timeout)
	Skipped Status = "SKIPPED" // not applicable, or a dependency failed
)

// weight orders statuses for ranking: root causes first.
func (s Status) weight() int {
	switch s {
	case Fail:
		return 0
	case Warn:
		return 1
	case Unknown:
		return 2
	case Skipped:
		return 3
	default:
		return 4
	}
}

type Result struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     Status   `json:"status"`
	Summary    string   `json:"summary"`
	Detail     string   `json:"detail,omitempty"`
	Confidence float64  `json:"confidence"` // 0..1 that this is the user's actual problem
	FixID      string   `json:"fix_id,omitempty"`
	FixHint    string   `json:"fix_hint,omitempty"`
	Refs       []string `json:"refs,omitempty"`
	Elevate    bool     `json:"needs_elevation,omitempty"` // re-running from an elevated terminal would help
}

type Probe interface {
	ID() string
	Title() string
	Milestone() string // "M1", "M2", "M3"
	Needs() []string   // probe IDs whose FAIL cascades to SKIPPED here
	Run(e *env.Env) Result
}

// Base carries the static parts so probes only implement Run.
type Base struct {
	PID        string
	PTitle     string
	PMilestone string
	PNeeds     []string
}

func (b Base) ID() string        { return b.PID }
func (b Base) Title() string     { return b.PTitle }
func (b Base) Milestone() string { return b.PMilestone }
func (b Base) Needs() []string   { return b.PNeeds }

// Res is a convenience constructor that copies identity from the Base.
func (b Base) Res(status Status, confidence float64, summary string) Result {
	return Result{ID: b.PID, Title: b.PTitle, Status: status, Confidence: confidence, Summary: summary}
}

// NeedsElevation builds the standard UNKNOWN result for admin-only data.
func (b Base) NeedsElevation(what string) Result {
	r := b.Res(Unknown, 0.1, fmt.Sprintf("%s cannot be read without elevation", what))
	r.FixHint = "run wslkit doctor from an elevated terminal (elevation is detected; no flag needed)"
	r.Elevate = true
	return r
}

// RunAll executes probes in order, applying the dependency cascade, then ranks.
// Every probe runs inside a recover so a panicking probe becomes an UNKNOWN
// result rather than a crashed run.
func RunAll(probes []Probe, e *env.Env) []Result {
	results := make([]Result, 0, len(probes))
	byID := map[string]Result{}
	for _, p := range probes {
		var r Result
		if dep, failed := failedDependency(p, byID); failed {
			r = Result{ID: p.ID(), Title: p.Title(), Status: Skipped, Confidence: 0,
				Summary: fmt.Sprintf("skipped: depends on %s, which failed", dep)}
		} else {
			r = safeRun(p, e)
		}
		if r.ID == "" {
			r.ID = p.ID()
		}
		if r.Title == "" {
			r.Title = p.Title()
		}
		results = append(results, r)
		byID[p.ID()] = r
	}
	Rank(results)
	return results
}

func failedDependency(p Probe, byID map[string]Result) (string, bool) {
	for _, dep := range p.Needs() {
		if r, ok := byID[dep]; ok && r.Status == Fail {
			return dep, true
		}
	}
	return "", false
}

func safeRun(p Probe, e *env.Env) (r Result) {
	defer func() {
		if rec := recover(); rec != nil {
			r = Result{ID: p.ID(), Title: p.Title(), Status: Unknown, Confidence: 0,
				Summary: "probe panicked; please report this with --json output",
				Detail:  fmt.Sprint(rec)}
		}
	}()
	return p.Run(e)
}

// Rank sorts in place: FAIL < WARN < UNKNOWN < SKIPPED < OK, then confidence
// descending, then ID.
func Rank(results []Result) {
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Status.weight() != b.Status.weight() {
			return a.Status.weight() < b.Status.weight()
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		return a.ID < b.ID
	})
}

// Filter keeps probes whose milestone is in the allowed set (empty = all).
func Filter(probes []Probe, milestones map[string]bool) []Probe {
	if len(milestones) == 0 {
		return probes
	}
	out := make([]Probe, 0, len(probes))
	for _, p := range probes {
		if milestones[p.Milestone()] {
			out = append(out, p)
		}
	}
	return out
}

// ExitCode maps results to the process exit code contract.
func ExitCode(results []Result) int {
	for _, r := range results {
		if r.Status == Fail {
			return 1
		}
	}
	return 0
}
