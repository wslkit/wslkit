package wsl

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// ZON001 reports the :Zone.Identifier files a saved download leaves behind.
//
// Windows attaches the mark of the web to a downloaded file as an alternate
// data stream. Save that file into \\wsl.localhost and the file server, which
// has no streams, writes it as a separate ordinary file named after the first
// with :Zone.Identifier on the end. Nothing on the Windows side shows it, so
// the first anyone hears of it is a wildcard matching twice as many files as it
// should, a build copying junk into an image, or a directory listing full of
// pairs.
//
// It is reported rather than fixed silently, and it is a warning rather than a
// failure: nothing is broken, but nobody wants these files and nothing else
// will ever remove them.

type ZoneFiles struct{}

func (ZoneFiles) base() probe.Base {
	return probe.Base{
		PID:        "ZON001",
		PTitle:     "Zone.Identifier files left by saved downloads",
		PMilestone: "M2",
		PNeeds:     []string{"WSL004"},
	}
}

func (p ZoneFiles) ID() string        { return p.base().ID() }
func (p ZoneFiles) Title() string     { return p.base().Title() }
func (p ZoneFiles) Milestone() string { return p.base().Milestone() }
func (p ZoneFiles) Needs() []string   { return p.base().Needs() }

func (p ZoneFiles) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Distros.OK() && !e.Distros.Absent() {
		return b.Res(probe.Unknown, 0.1, "could not read the distribution inventory: "+e.Distros.Err)
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.OK, 0, "No distributions are registered, so there is nothing to scan")
	}

	var findings, details, skipped []string
	scanned, total := 0, 0
	truncated := false

	for _, d := range distros {
		if d.Version != 2 {
			continue
		}
		if !d.ZoneFiles.OK() {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", d.Name, reasonForZone(d.ZoneFiles)))
			continue
		}
		scanned++
		s := d.ZoneFiles.Value
		if s.Truncated {
			truncated = true
		}
		if s.Count == 0 {
			continue
		}
		total += s.Count
		count := fmt.Sprintf("%d", s.Count)
		if s.Truncated {
			// The walk stopped early, so the number is a floor. Saying so
			// matters: the fix will find more than this.
			count = "at least " + count
		}
		findings = append(findings, fmt.Sprintf("%s: %s Zone.Identifier file(s)", d.Name, count))
		details = append(details, fmt.Sprintf("%s: %s file(s) under /home and /root%s",
			d.Name, count, examples(s.Paths)))
	}

	if scanned == 0 && len(skipped) > 0 {
		r := b.Res(probe.Skipped, 0, fmt.Sprintf("No running distribution to scan (%s)", strings.Join(skipped, "; ")))
		r.Detail = "These files can only be found by reading the distribution from Windows, which starts it if it is stopped. Start one and run the check again, or use --allow-vm-wake."
		return r
	}

	if len(findings) == 0 {
		msg := fmt.Sprintf("No Zone.Identifier files under /home or /root in %d distribution(s)", scanned)
		if len(skipped) > 0 {
			msg += fmt.Sprintf("; %d not running", len(skipped))
		}
		r := b.Res(probe.OK, 0, msg)
		if truncated {
			// A clean result from a walk that gave up early is worth less
			// than a clean result from one that finished.
			r.Confidence = 0.5
			r.Detail = "The scan reached its own limit before it finished, so a file deeper in the tree could have been missed."
		}
		return r
	}

	r := b.Res(probe.Warn, 0.5, strings.Join(findings, "; "))
	r.Detail = strings.Join(details, "\n") +
		"\nWindows records where a downloaded file came from in an alternate data stream. " +
		"The file server behind \\\\wsl.localhost has no streams, so saving a download into the " +
		"distribution writes that record as a second ordinary file. Deleting them loses only " +
		"that record; the downloads themselves are untouched."
	r.FixHint = fmt.Sprintf("wslkit fix zone --distro <name> (%d file(s) found)", total)
	return r
}

// examples renders a few paths, which is what turns a count into something the
// reader recognises.
func examples(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	show := paths
	more := ""
	if len(show) > 3 {
		show, more = show[:3], fmt.Sprintf(", and %d more", len(paths)-3)
	}
	return ", for example " + strings.Join(show, ", ") + more
}

// reasonForZone turns the recorded provenance into something a reader can act
// on. A truncated scan is not in here: that is a result, not a reason to skip.
func reasonForZone(f env.Field[env.ZoneScan]) string {
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
