// Package guard gets WSL working again after the machine has slept.
//
// WSL handles no power events at all. The service helper registers only the
// low-power-epoch notification, and only for a service that implements the
// corresponding callback, which WslService does not; PBT_APMSUSPEND and
// PBT_APMRESUMEAUTOMATIC fall through to the default case. The Linux init has
// no resume hook either. So nothing in the product knows the machine slept, and
// the AF_HYPERV sockets torn down entering Modern Standby are never rebuilt.
//
// What that looks like is a distribution that hangs on the first command after
// the lid opens, sometimes until the machine is restarted.
//
// The recovery is a ladder, and the order matters more than any individual
// rung: each step is more disruptive than the one before it, so the cheap ones
// are tried first and the expensive ones only on evidence that the cheap ones
// did not work. Two things are never touched, whatever happens: vmcompute and
// HvHost. Stopping vmcompute has been reported to bluescreen the machine, and
// no amount of a hung distribution justifies that.
package guard

import (
	"fmt"
	"time"
)

// Health is what the probes found.
type Health struct {
	// ServiceAnswered is whether `wsl --status` returned in time. When it
	// does not, the service itself is wedged, which is the strongest single
	// signal there is.
	ServiceAnswered bool
	// ServiceHung distinguishes a timeout from an error. A service that
	// answers "not installed" is not the same as one that never answers.
	ServiceHung bool
	// DistroAnswered is whether a trivial command inside the default
	// distribution returned in time.
	DistroAnswered bool
	// DistroHung is the same distinction for the distribution.
	DistroHung bool
	// Distro is the distribution that was probed, for the log.
	Distro string
	// WasRunning records whether anything was running before the machine
	// slept. A distribution that was not running before is not broken now:
	// it is merely stopped, and starting it from cold is slow.
	WasRunning bool
	// WasRunningKnown says whether the answer above was recorded rather than
	// assumed. Without it, escalation has no evidence to stand on.
	WasRunningKnown bool
	// Skew is the guest clock minus the host clock, when both were read.
	Skew time.Duration
	// SkewKnown says whether Skew means anything.
	SkewKnown bool
	// Err is what went wrong probing, if anything.
	Err error
}

// Healthy reports whether anything needs doing at all.
func (h Health) Healthy() bool { return h.ServiceAnswered && h.DistroAnswered }

// Rung is one step of the recovery ladder.
type Rung int

const (
	// RungNone is the healthy case.
	RungNone Rung = iota
	// RungShutdown asks WSL to stop the VM the ordinary way. It works when
	// the service still answers, and hangs when the vsock is dead.
	RungShutdown
	// RungForceShutdown terminates the compute system directly, which is
	// what `wsl --shutdown --force` does. It does not need the vsock.
	RungForceShutdown
	// RungKillService kills wslservice.exe. This is the most-reported
	// working step upstream, and it needs administrator rights.
	RungKillService
	// RungRestartService restarts the service properly afterwards. Reports
	// of it working on its own are mixed, so it is last rather than first.
	RungRestartService
)

func (r Rung) String() string {
	switch r {
	case RungNone:
		return "nothing"
	case RungShutdown:
		return "wsl --shutdown"
	case RungForceShutdown:
		return "wsl --shutdown --force"
	case RungKillService:
		return "kill wslservice.exe"
	case RungRestartService:
		return "restart WSLService"
	}
	return "unknown"
}

// Disruptive reports whether a rung stops the user's work rather than merely
// prodding the service.
func (r Rung) Disruptive() bool { return r >= RungShutdown }

// NeedsElevation reports whether a rung can only be taken by an administrator.
func (r Rung) NeedsElevation() bool { return r >= RungKillService }

// Options controls what the recovery is allowed to do.
type Options struct {
	// Elevated says the process has administrator rights, which is what the
	// last two rungs need.
	Elevated bool
	// MaxRung caps how far to climb. Zero means the whole ladder.
	MaxRung Rung
	// SkewThreshold is how far the guest clock may drift before it is worth
	// correcting. Chrony slews rather than steps after its first three
	// updates, so a long sleep can leave an offset that never closes.
	SkewThreshold time.Duration
	// DryRun decides everything and does nothing.
	DryRun bool
}

// DefaultSkewThreshold is where a drifting guest clock starts breaking things:
// TLS certificate validation, build timestamps, anything checking a signature's
// validity window.
const DefaultSkewThreshold = 5 * time.Second

// Decision is what to do about one health reading.
type Decision struct {
	// Ladder is the rungs to try, in order, re-probing between them.
	Ladder []Rung
	// FixClock says the guest clock needs stepping.
	FixClock bool
	// Reason is the sentence that goes in the log, and it is the point of
	// this whole type: an unattended recovery that cannot say why it
	// restarted somebody's work is one nobody will leave enabled.
	Reason string
}

// Decide works out what the health reading calls for.
//
// The rule that keeps this from being a nuisance: a distribution that was not
// running before the machine slept is not broken, it is stopped, and starting
// one from cold takes long enough to look like a hang. Escalating on that would
// mean a scheduled task shutting down WSL every time the lid opens.
func Decide(h Health, o Options) Decision {
	if o.SkewThreshold <= 0 {
		o.SkewThreshold = DefaultSkewThreshold
	}
	fixClock := h.SkewKnown && (h.Skew > o.SkewThreshold || h.Skew < -o.SkewThreshold)

	switch {
	case h.Healthy():
		d := Decision{FixClock: fixClock, Reason: "WSL answered; nothing to recover"}
		if fixClock {
			d.Reason = fmt.Sprintf("WSL answered, but the guest clock is %s out", skew(h.Skew))
		}
		return d

	case !h.ServiceAnswered && !h.ServiceHung:
		// The service answered, with a refusal. WSL not being installed, or
		// the runtime being broken in some way that has a message attached,
		// is not something restarting things fixes: the ladder would stop a
		// VM that is not running to cure an error that is still there
		// afterwards.
		return Decision{
			FixClock: fixClock,
			Reason:   fmt.Sprintf("wsl --status failed rather than hung (%v); that is not something restarting WSL fixes", h.Err),
		}

	case h.ServiceHung:
		// The service itself is not answering, so asking it to shut down
		// politely is unlikely to work and the ladder starts anyway: the
		// polite rung is cheap and sometimes returns.
		return Decision{
			Ladder:   cap(fullLadder, o),
			FixClock: fixClock,
			Reason:   "wsl --status did not answer: the service is wedged",
		}

	case !h.DistroAnswered && !h.WasRunningKnown:
		// Something is wrong, but there is no record of what was running
		// before the machine slept, so this could equally be a cold start.
		// Stopping the VM on that guess is the behaviour that gets an
		// unattended tool uninstalled.
		return Decision{
			FixClock: fixClock,
			Reason:   fmt.Sprintf("%s did not answer, but nothing recorded whether it was running before the machine slept, so this may just be a cold start", h.Distro),
		}

	case !h.DistroAnswered && !h.WasRunning:
		return Decision{
			FixClock: fixClock,
			Reason:   fmt.Sprintf("%s did not answer, but it was not running before the machine slept: starting one from cold is slow, not broken", h.Distro),
		}

	case !h.DistroAnswered:
		return Decision{
			Ladder:   cap(fullLadder, o),
			FixClock: fixClock,
			Reason:   fmt.Sprintf("%s was running before the machine slept and does not answer now", h.Distro),
		}
	}

	return Decision{FixClock: fixClock, Reason: "nothing to do"}
}

// fullLadder is every rung, in the order they are tried.
var fullLadder = []Rung{RungShutdown, RungForceShutdown, RungKillService, RungRestartService}

// cap trims the ladder to what the options permit.
//
// A rung that needs administrator rights is dropped rather than attempted and
// failed: a log full of access-denied lines from a task that was never going to
// work teaches the reader to ignore the log.
func cap(ladder []Rung, o Options) []Rung {
	var out []Rung
	for _, r := range ladder {
		if o.MaxRung != RungNone && r > o.MaxRung {
			continue
		}
		if r.NeedsElevation() && !o.Elevated {
			continue
		}
		out = append(out, r)
	}
	return out
}

// skew renders a clock offset the way a log should read it.
func skew(d time.Duration) string {
	if d < 0 {
		return d.Abs().Round(time.Second).String() + " behind"
	}
	return d.Round(time.Second).String() + " ahead"
}

// ParseRung reads a rung name, for the command line.
func ParseRung(s string) (Rung, bool) {
	switch s {
	case "", "all":
		return RungNone, true
	case "shutdown":
		return RungShutdown, true
	case "force":
		return RungForceShutdown, true
	case "kill":
		return RungKillService, true
	case "restart":
		return RungRestartService, true
	}
	return RungNone, false
}
