package top

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// HostVM is one VM as Windows sees it: a vmmem process, whose working set is
// what Task Manager charges to that VM.
type HostVM struct {
	PID             uint32
	Created         time.Time
	WorkingSetBytes uint64
}

// HostReader lists the VMs Windows is running. A Runner that also implements
// it gets the Windows side of the report; one that does not gets the guest
// side alone.
type HostReader interface {
	HostVMs(ctx context.Context) ([]HostVM, error)
}

// Host is the Windows side of a report.
type Host struct {
	// Utility is the utility VM's vmmem, when exactly one matched it.
	Utility *HostVM
	// Others are every other vmmem: a wslc session, or any other Hyper-V VM.
	// Which one cannot be told without elevation.
	Others []HostVM
	// Err says why the Windows side could not be read.
	Err error
}

// matchTolerance is how far apart a vmmem's creation and its guest's boot may
// be. Measured at 0.5 s and 1.2 s on the two VMs of one machine, and a sweep
// adds its own delay on top.
const matchTolerance = 3 * time.Second

// MatchHost finds the utility VM among the vmmem processes by when it started.
//
// Nothing else identifies a vmmem without elevation: its owner is the VM's
// virtual account and GetOwner is denied, and hcsdiag needs Hyper-V
// administrator rights. Its creation time is readable, and it is the moment
// the VM booted, which the guest reports as its uptime. Two VMs booted within
// the tolerance of each other are ambiguous, and then neither is claimed.
func MatchHost(vms []HostVM, utilityBoot time.Time) Host {
	var h Host
	h.Utility, h.Others = claim(vms, utilityBoot)
	return h
}

// claim takes the one vmmem created at boot out of vms. None is taken when
// none matches or when more than one does.
func claim(vms []HostVM, boot time.Time) (*HostVM, []HostVM) {
	match := -1
	for i, v := range vms {
		d := v.Created.Sub(boot)
		if d < 0 {
			d = -d
		}
		if d <= matchTolerance {
			if match >= 0 {
				return nil, vms
			}
			match = i
		}
	}
	if match < 0 {
		return nil, vms
	}
	found := vms[match]
	rest := append(append([]HostVM(nil), vms[:match]...), vms[match+1:]...)
	return &found, rest
}

// claimSessions gives each measured wslc session its vmmem, so it is shown
// with its session rather than as an unnamed other VM.
//
// A matched vmmem also settles whether this measurement started the VM: its
// creation time and the sweep's start are both read off the Windows clock, so
// comparing them does not depend on the guest's.
func claimSessions(h *Host, sessions []Session, sampledAt, sweepStarted time.Time) {
	for i := range sessions {
		at := sessions[i].sampledAt
		if at.IsZero() {
			at = sampledAt
		}
		boot, ok := sessions[i].Boot(at)
		if !ok || sessions[i].Err != nil {
			continue
		}
		sessions[i].Host, h.Others = claim(h.Others, boot)
		if v := sessions[i].Host; v != nil && !sweepStarted.IsZero() {
			sessions[i].Started = v.Created.After(sweepStarted)
		}
	}
}

// UtilityBoot is when the utility VM booted, by its own clock: the moment it
// was sampled, less its uptime at that moment.
func (r Report) UtilityBoot() (time.Time, bool) {
	if r.SampledAt.IsZero() || r.VM.AtCsec == 0 {
		return time.Time{}, false
	}
	return r.SampledAt.Add(-time.Duration(r.VM.AtCsec) * 10 * time.Millisecond), true
}

// readHost fills in the Windows side of a report, if the runner can.
func readHost(ctx context.Context, r Runner, report *Report) {
	hr, ok := r.(HostReader)
	if !ok {
		return
	}
	vms, err := hr.HostVMs(ctx)
	if err != nil {
		report.Host = &Host{Err: err}
		return
	}
	// With no distribution answering there is no boot time to match, and
	// usually no utility VM either; every vmmem is then some other VM.
	h := Host{Others: vms}
	if boot, ok := report.UtilityBoot(); ok {
		h = MatchHost(vms, boot)
	}
	claimSessions(&h, report.Sessions, report.SampledAt, report.StartedAt)
	report.Host = &h
}

// ParseCIMDateTime reads a WMI datetime, yyyymmddHHMMSS.mmmmmmsUUU, where the
// last four characters are the offset from UTC in minutes.
func ParseCIMDateTime(s string) (time.Time, error) {
	if len(s) != 25 || s[14] != '.' || (s[21] != '+' && s[21] != '-') {
		return time.Time{}, fmt.Errorf("top: %q is not a WMI datetime", s)
	}
	t, err := time.Parse("20060102150405.000000", s[:21])
	if err != nil {
		return time.Time{}, fmt.Errorf("top: %q is not a WMI datetime: %w", s, err)
	}
	off, err := strconv.Atoi(s[22:])
	if err != nil {
		return time.Time{}, fmt.Errorf("top: %q is not a WMI datetime: %w", s, err)
	}
	if s[21] == '-' {
		off = -off
	}
	// t was read as UTC; the wall clock it names is off minutes ahead of UTC.
	return t.Add(-time.Duration(off) * time.Minute), nil
}
