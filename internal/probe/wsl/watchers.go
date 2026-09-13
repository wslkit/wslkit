package wsl

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// MNT001 reports a file watcher working on the Windows filesystem.
//
// inotify is a kernel facility over a real Linux filesystem. /mnt/c is not one:
// it is a protocol to a server on the Windows side, and that server never says
// a file changed. A watch set there is accepted, and then never fires.
//
// Nothing anywhere reports this. The dev server sits there, the page does not
// reload, and the conclusion drawn is that the tool is broken or that WSL is
// slow. Upstream has said plainly that user land cannot fix it, so what is left
// is to notice the situation and name the setting that makes the tool poll.
//
// It is a warning, not a failure. The setup works; it is the watching that does
// not, and only for the files that are on the Windows side.

type Watchers struct{}

func (Watchers) base() probe.Base {
	return probe.Base{
		PID:        "MNT001",
		PTitle:     "File watchers working on the Windows filesystem",
		PMilestone: "M2",
		PNeeds:     []string{"WSL004"},
	}
}

func (p Watchers) ID() string        { return p.base().ID() }
func (p Watchers) Title() string     { return p.base().Title() }
func (p Watchers) Milestone() string { return p.base().Milestone() }
func (p Watchers) Needs() []string   { return p.base().Needs() }

func (p Watchers) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Distros.OK() && !e.Distros.Absent() {
		return b.Res(probe.Unknown, 0.1, "could not read the distribution inventory: "+e.Distros.Err)
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.OK, 0, "No distributions are registered, so nothing is watching anything")
	}
	tbl, err := data.LoadWatchers()
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "the watcher table could not be loaded: "+err.Error())
	}

	var findings, details, skipped []string
	checked := 0
	for _, d := range distros {
		if d.Version != 2 {
			continue
		}
		if !d.Watchers.OK() {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", d.Name, reasonForWatchers(d.Watchers)))
			continue
		}
		checked++
		for _, w := range d.Watchers.Value {
			findings = append(findings, fmt.Sprintf("%s: %s is watching %s", d.Name, w.Name, w.Dir))
			line := fmt.Sprintf("%s: %s in %s", d.Name, w.Name, w.Dir)
			if t, ok := tbl.LookupWatcher(w.Name); ok {
				line += "\n    Switch it to polling: " + t.Knob
				if t.Note != "" {
					line += "\n    " + t.Note
				}
			}
			details = append(details, line)
		}
	}

	if checked == 0 && len(skipped) > 0 {
		r := b.Res(probe.Skipped, 0, fmt.Sprintf("No running distribution to look in (%s)", strings.Join(skipped, "; ")))
		r.Detail = "What is running inside a distribution can only be read while it is running. Start one and run the check again."
		return r
	}

	if len(findings) == 0 {
		msg := fmt.Sprintf("No file watchers are working on a Windows drive in %d distribution(s)", checked)
		if len(skipped) > 0 {
			msg += fmt.Sprintf("; %d not running", len(skipped))
		}
		return b.Res(probe.OK, 0, msg)
	}

	// A machine mid-build can have a dozen of these. The summary names the
	// first few and counts the rest; the detail has all of them.
	summary := findings
	if len(summary) > 3 {
		summary = append(append([]string(nil), summary[:3]...),
			fmt.Sprintf("and %d more", len(findings)-3))
	}
	r := b.Res(probe.Warn, 0.6, strings.Join(summary, "; "))
	r.Detail = strings.Join(details, "\n") + "\n\n" +
		"A watch on /mnt never fires. inotify is a kernel facility over a Linux filesystem; " +
		"the Windows drives are served over a protocol that does not report changes, so the watch " +
		"is accepted and then silently does nothing. This cannot be fixed from inside the " +
		"distribution (microsoft/WSL#4739). Either switch the tool to polling, which costs CPU but " +
		"works, or move the tree onto the distribution's own filesystem, which is faster as well."
	r.FixHint = "move the project into the distribution (~/), or set the polling option listed above"
	return r
}

func reasonForWatchers(f env.Field[[]env.WatchProc]) string {
	switch f.ErrKind {
	case env.ErrVMWakeRefused:
		return "not running"
	case env.ErrTimeout:
		return "timed out"
	case env.ErrNeedsElevation:
		return "access denied"
	case env.ErrUnsupported:
		return "not applicable"
	default:
		return f.Err
	}
}
