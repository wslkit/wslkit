// Package evt holds event-log correlation probes (EVT***, PWR***).
package evt

import (
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/probe"
)

func All() []probe.Probe { return []probe.Probe{Crashes{}} }

type Crashes struct{}

func (Crashes) base() probe.Base {
	return probe.Base{PID: "EVT001", PTitle: "Recent WSL crashes and power events", PMilestone: "M2"}
}
func (p Crashes) ID() string        { return p.base().ID() }
func (p Crashes) Title() string     { return p.base().Title() }
func (p Crashes) Milestone() string { return p.base().Milestone() }
func (p Crashes) Needs() []string   { return nil }

func (p Crashes) Run(e *env.Env) probe.Result {
	b := p.base()
	rec := e.Events.Recent
	if !rec.OK() {
		if rec.NeedsElevation() {
			return b.NeedsElevation("event log")
		}
		return b.Res(probe.Unknown, 0.05, "event log not collected: "+rec.Err)
	}
	var crashes, resumes, vmswitch []env.Event
	for _, ev := range rec.Value {
		switch {
		case ev.Provider == "Application Error" || ev.Provider == "Windows Error Reporting":
			crashes = append(crashes, ev)
		case ev.Provider == "Microsoft-Windows-Kernel-Power" && (ev.ID == 107 || ev.ID == 42):
			resumes = append(resumes, ev)
		case strings.Contains(ev.Channel, "VmSwitch"):
			vmswitch = append(vmswitch, ev)
		}
	}
	window := e.Events.Window
	if window == 0 {
		window = 7 * 24 * time.Hour
	}
	var lines []string
	for _, c := range crashes {
		lines = append(lines, fmt.Sprintf("%s  %s  %s", c.Time.Local().Format("2006-01-02 15:04"), c.Provider, firstLine(c.Message)))
	}
	if len(crashes) > 0 {
		r := b.Res(probe.Warn, 0.6, fmt.Sprintf("%d WSL process crash report(s) in the last %s", len(crashes), humanDuration(window)))
		r.Detail = strings.Join(lines, "\n")
		if len(resumes) > 0 {
			last := resumes[0]
			r.Detail += fmt.Sprintf("\nLast sleep/resume event: %s (id %d)", last.Time.Local().Format("2006-01-02 15:04"), last.ID)
			if crashAfterResume(crashes, resumes) {
				r.Detail += "\nA crash followed a resume within 10 minutes: this matches the 'WSL unresponsive after sleep/hibernate' class."
				r.Refs = append(r.Refs, "https://github.com/microsoft/WSL/issues/8696")
				r.FixHint = "wsl --shutdown before sleeping, or set [wsl2] vmIdleTimeout lower; dumps are in %TEMP%\\wsl-crashes"
			}
		}
		if r.FixHint == "" {
			r.FixHint = "crash dumps are in %TEMP%\\wsl-crashes; attach them to a microsoft/WSL issue with this report"
		}
		return r
	}
	r := b.Res(probe.OK, 0.4, fmt.Sprintf("No WSL crash reports in the last %s", humanDuration(window)))
	if len(vmswitch) > 0 {
		r.Detail = fmt.Sprintf("%d Hyper-V VmSwitch error/critical event(s) in the window", len(vmswitch))
	}
	return r
}

func crashAfterResume(crashes, resumes []env.Event) bool {
	for _, c := range crashes {
		for _, r := range resumes {
			if r.ID == 107 && c.Time.After(r.Time) && c.Time.Sub(r.Time) < 10*time.Minute {
				return true
			}
		}
	}
	return false
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return d.Round(time.Minute).String()
	}
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
