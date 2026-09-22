package top

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// KindWSLC is a container in a wslc session.
const KindWSLC Kind = "wslc"

// Session is one wslc session: a VM of its own, separate from the utility VM,
// and the containers running in it.
//
// wslc exists only in the WSL 2.9 pre-releases and its output changes from one
// release to the next, so this is read only when asked for with --wslc. Asking
// is also what makes it safe: `wslc list` boots a stopped session VM, and
// nothing a normal user can read says whether it is up without asking. See
// docs/research/2026-09-wslc-session.md.
type Session struct {
	Name       string
	VM         VM
	Containers []Sample
	// Host is the session VM's vmmem, when exactly one matched it.
	Host *HostVM
	// Started says the VM booted during this measurement: it was not running,
	// and asking wslc started it.
	Started bool
	Err     error

	// sampledAt is when this session's measurement came back, by the Windows
	// clock, so its boot time is not skewed by the sessions measured with it.
	sampledAt time.Time
}

// SessionReader reads wslc sessions. A Runner that also implements it can
// measure them.
type SessionReader interface {
	// Sessions lists the sessions wslc knows. It does not start one.
	Sessions(ctx context.Context) ([]string, error)
	// SampleSession runs SessionScript inside the session's VM.
	SampleSession(ctx context.Context, name string, timeout time.Duration) (string, error)
	// ContainerNames is `wslc list --format json` for the session.
	ContainerNames(ctx context.Context, name string) (string, error)
}

// ParseSession reads what SessionScript printed, naming each container from
// what `wslc list --format json` printed.
func ParseSession(name, out, list string) (Session, error) {
	s := Session{Name: name}
	own, groups, order := fields(out)
	if len(own) == 0 {
		return s, fmt.Errorf("top: wslc session %s printed nothing that could be read", name)
	}
	f := reader(own)
	hz := f.num("clock_hz")
	if hz == 0 {
		hz = 100
	}
	s.VM = f.vm(hz)
	names := containerNames(list)
	for _, id := range order {
		g := reader(groups[id])
		if _, ok := g["memory_bytes"]; !ok {
			continue
		}
		c := Sample{Distro: shortID(id), Kind: KindWSLC, Method: MethodCgroup, CgroupPath: "/docker/" + id}
		for short, n := range names {
			if strings.HasPrefix(id, short) {
				c.Distro = n
				break
			}
		}
		g.cgroupCounters(&c)
		s.Containers = append(s.Containers, c)
	}
	return s, nil
}

// containerNames maps a container ID, as `wslc list` truncates it, to its
// name. The output is one JSON object per line. A line that does not parse is
// skipped: a container is then shown by its ID rather than not at all.
func containerNames(list string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var c struct {
			ID    string `json:"ID"`
			Names string `json:"Names"`
		}
		if json.Unmarshal([]byte(line), &c) != nil || c.ID == "" {
			continue
		}
		name := c.Names
		if name == "" {
			name = shortID(c.ID)
		}
		out[c.ID] = name
	}
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Boot is when the session VM booted, by its own clock.
func (s Session) Boot(sampledAt time.Time) (time.Time, bool) {
	if sampledAt.IsZero() || s.VM.AtCsec == 0 {
		return time.Time{}, false
	}
	return sampledAt.Add(-time.Duration(s.VM.AtCsec) * 10 * time.Millisecond), true
}

// sweepSessions measures every wslc session. Each is its own VM, so they are
// measured at once, like distributions.
func sweepSessions(ctx context.Context, sr SessionReader, timeout time.Duration, started time.Time) ([]Session, error) {
	names, err := sr.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(names))
	done := make(chan struct{}, len(names))
	for i, name := range names {
		go func(i int, name string) {
			defer func() { done <- struct{}{} }()
			out, err := sr.SampleSession(ctx, name, timeout)
			at := time.Now()
			if err != nil {
				sessions[i] = Session{Name: name, Err: err}
				return
			}
			// Names are a convenience: without them a container is shown by
			// its ID, which is still the right row.
			list, _ := sr.ContainerNames(ctx, name)
			s, err := ParseSession(name, out, list)
			s.Err = err
			s.sampledAt = at
			// The guest's uptime can only put its boot at or after the
			// moment the VM was created, so a boot after the sweep began is
			// one this sweep caused. claimSessions refines this with the
			// vmmem's own creation time when it can.
			if boot, ok := s.Boot(at); ok && boot.After(started) {
				s.Started = true
			}
			sessions[i] = s
		}(i, name)
	}
	for range names {
		<-done
	}
	return sessions, nil
}

// sessionRates fills in what changed in each session since before.
func sessionRates(before, after []Session, interval time.Duration) []Session {
	prev := map[string]Session{}
	for _, s := range before {
		prev[s.Name] = s
	}
	out := make([]Session, len(after))
	for i, s := range after {
		b, ok := prev[s.Name]
		if ok && b.Err == nil && s.Err == nil {
			s.Containers = ratesFor(b.Containers, s.Containers, interval)
			s.VM.Rates = vmRates(b.VM, s.VM, interval)
			s.Started = s.Started || b.Started
		}
		out[i] = s
	}
	return out
}

// renderSessions writes one section per wslc session.
func renderSessions(w io.Writer, r Report) {
	for _, s := range r.Sessions {
		fmt.Fprintln(w)
		if s.Err != nil {
			fmt.Fprintf(w, "wslc session %s: could not be measured: %v\n", s.Name, s.Err)
			continue
		}
		var host *Host
		if s.Host != nil {
			host = &Host{Utility: s.Host}
		}
		renderVM(w, "wslc session "+s.Name, s.VM, host)
		fmt.Fprintln(w)
		if len(s.Containers) == 0 {
			fmt.Fprintln(w, "no containers are running")
		} else {
			fmt.Fprint(w, rowsTable(Sorted(s.Containers), true, r.HasRates()).String())
		}
		if s.Started {
			fmt.Fprintf(w, "note: the session VM was not running, and asking wslc started it. It stops again once idle.\n")
		}
		for _, c := range s.Containers {
			if c.OOMKills != nil && *c.OOMKills > 0 {
				fmt.Fprintf(w, "note: %s: the kernel has killed %d process(es) for running out of memory\n", c.Distro, *c.OOMKills)
			}
		}
	}
	if len(r.Sessions) > 0 {
		fmt.Fprintln(w, "\n"+sessionsNote)
	}
}

// sessionsNote says what the wslc sections are and how far to trust them.
const sessionsNote = "wslc is a preview in the WSL 2.9 pre-releases. Each session runs its containers in a VM of its own, which is why it has its own totals."

// sessionsJSON is the wslc_sessions list of --json.
func sessionsJSON(sessions []Session) []map[string]any {
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		o := map[string]any{"name": s.Name}
		if s.Err != nil {
			o["error"] = s.Err.Error()
			out = append(out, o)
			continue
		}
		containers := make([]map[string]any, 0, len(s.Containers))
		for _, c := range Sorted(s.Containers) {
			containers = append(containers, sampleJSON(c, "name"))
		}
		o["vm"] = vmJSON(s.VM)
		o["containers"] = containers
		o["started_by_measurement"] = s.Started
		if s.Host != nil {
			o["host"] = hostVMJSON(*s.Host)
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool { return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"]) })
	return out
}
