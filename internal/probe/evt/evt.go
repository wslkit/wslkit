// Package evt holds event-log correlation probes (EVT***, PWR***).
package evt

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
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
	var crashes, resumes, vmswitch, platform []env.Event
	for _, ev := range rec.Value {
		switch {
		case ev.Provider == "Application Error" || ev.Provider == "Windows Error Reporting":
			crashes = append(crashes, ev)
		case ev.Provider == "Microsoft-Windows-Kernel-Power" && (ev.ID == 107 || ev.ID == 42):
			resumes = append(resumes, ev)
		case strings.Contains(ev.Channel, "VmSwitch"):
			vmswitch = append(vmswitch, ev)
		case strings.Contains(ev.Channel, "Hyper-V-Compute"), strings.Contains(ev.Channel, "Host-Network-Service"):
			// Why the VM refused to start, and why its network could not
			// be built. These are the two logs that say what wsl.exe only
			// hints at with an HRESULT, and they are readable only by an
			// administrator.
			platform = append(platform, ev)
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

	// The platform channels are only readable elevated, so a quiet result
	// from an ordinary console means "nobody looked", not "nothing there".
	// Saying which is the difference between a check that is trusted and one
	// that quietly under-reports.
	locked := lockedChannels(e)
	platformNote := ""
	if len(platform) > 0 {
		// Counted and shown, but not led with: see the rule below. Somebody
		// investigating a real problem still wants to know these are here.
		platformNote = fmt.Sprintf("\n%d Hyper-V compute / host network error(s) in the window, none of the shape that stops WSL starting:\n%s",
			len(platform), describePlatform(platform))
	} else if len(locked) > 0 {
		platformNote = fmt.Sprintf("\nNot read (needs an elevated console): %s. These say why a VM refused to start and why its network could not be configured.", strings.Join(locked, ", "))
	}

	// A machine that works logs host network errors anyway. Measured on a
	// healthy desktop: 23 Host-Network-Service events in a week, every one id
	// 1006 carrying 0x80070002, on a machine where WSL starts every time.
	// Leading with those, and attaching advice to restart HNS and delete the
	// WSL network, would send somebody to fix nothing.
	//
	// The compute channel is different. It logs when a virtual machine failed
	// to start, which is the thing this tool exists to explain, so it leads on
	// its own. A network error leads when it carries an HRESULT known to stop
	// WSL starting, or when something crashed in the same window that it might
	// explain. Everything else is counted in the detail, where somebody
	// investigating can still find it.
	if len(platform) > 0 && (anyCompute(platform) || knownFatal(platform) || len(crashes) > 0) {
		r := b.Res(probe.Warn, 0.65, fmt.Sprintf("%d Hyper-V compute / host network error(s) in the last %s", len(platform), humanDuration(window)))
		r.Detail = describePlatform(platform)
		if len(crashes) > 0 {
			r.Detail += fmt.Sprintf("\n\n%d WSL process crash report(s) in the same window:\n%s", len(crashes), strings.Join(lines, "\n"))
		}
		if knownFatal(platform) {
			r.Detail += "\nAn error of this shape is the known 'WSL will not start after a network change' class: the VM's network cannot be built, and wsl.exe reports 0x8007054f without saying why."
			r.Refs = append(r.Refs, "https://github.com/microsoft/WSL/issues/13454")
			r.FixHint = "wsl --shutdown, then restart the Host Network Service (net stop hns && net start hns, admin); if it persists, remove the WSL network: Get-HnsNetwork | Where-Object Name -eq WSL | Remove-HnsNetwork"
		}
		return r
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
		r.Detail = join(r.Detail, platformNote)
		return r
	}
	r := b.Res(probe.OK, 0.4, fmt.Sprintf("No WSL crash reports in the last %s", humanDuration(window)))
	if len(vmswitch) > 0 {
		r.Detail = fmt.Sprintf("%d Hyper-V VmSwitch error/critical event(s) in the window", len(vmswitch))
	}
	if len(locked) > 0 {
		// A clean result from logs nobody could open is worth less than a
		// clean result from logs that were read.
		r.Confidence = 0.25
	}
	r.Detail = join(r.Detail, platformNote)
	return r
}

// join puts two blocks together without leaving a blank line where the first
// one was empty.
func join(a, b string) string {
	switch {
	case b == "":
		return a
	case a == "":
		return strings.TrimPrefix(b, "\n")
	}
	return a + b
}

// lockedChannels lists the channels this run could not open for want of
// elevation, shortened to the part that identifies them.
func lockedChannels(e *env.Env) []string {
	var out []string
	for name, f := range e.Events.Channels {
		if f.ErrKind != env.ErrNeedsElevation {
			continue
		}
		if strings.Contains(name, "Hyper-V-Compute") || strings.Contains(name, "Host-Network-Service") {
			out = append(out, strings.TrimPrefix(name, "Microsoft-Windows-"))
		}
	}
	sort.Strings(out)
	return out
}

// describePlatform renders the compute and network errors, newest first.
func describePlatform(evs []env.Event) string {
	sorted := append([]env.Event(nil), evs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.After(sorted[j].Time) })
	const show = 8
	var lines []string
	for i, ev := range sorted {
		if i == show {
			lines = append(lines, fmt.Sprintf("  ... and %d more", len(sorted)-show))
			break
		}
		lines = append(lines, fmt.Sprintf("  %s  %s id %d  %s",
			ev.Time.Local().Format("2006-01-02 15:04"),
			strings.TrimPrefix(ev.Channel, "Microsoft-Windows-"), ev.ID, firstLine(ev.Message)))
	}
	return strings.Join(lines, "\n")
}

// fatalCodes are the HRESULTs that mean the VM or its network could not be
// built, rather than something the platform logged and recovered from.
//
// 0x8007054f is ERROR_INTERNAL_ERROR wrapped as an HRESULT, which tells nobody
// anything on its own; in this company it means the network. Being on the
// Host-Network-Service channel is deliberately not enough on its own: a healthy
// machine logs 0x80070002 there several times a week, and treating the channel
// as the signal turns that into a finding with a remedy attached.
var fatalCodes = []string{"0x8007054f", "0x80370102", "0x80070422"}

// knownFatal reports whether any event carries one of them.
func knownFatal(evs []env.Event) bool {
	for _, ev := range evs {
		m := strings.ToLower(ev.Message)
		for _, code := range fatalCodes {
			if strings.Contains(m, code) {
				return true
			}
		}
	}
	return false
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

// anyCompute reports whether any event came from the Hyper-V compute service,
// which is what logs a virtual machine failing to start.
func anyCompute(evs []env.Event) bool {
	for _, ev := range evs {
		if strings.Contains(ev.Channel, "Hyper-V-Compute") {
			return true
		}
	}
	return false
}
