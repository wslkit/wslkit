package guard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Probe timeouts. Each is the point past which waiting longer tells you
// nothing: a service that has not answered in ten seconds is not thinking.
const (
	StatusTimeout   = 10 * time.Second
	DistroTimeout   = 20 * time.Second
	ShutdownTimeout = 30 * time.Second
	// SettleDelay is how long to wait after a resume before probing at all.
	// The network stack, the hypervisor and the service are all coming back
	// at once, and a probe fired into the middle of that measures the
	// resume rather than WSL.
	SettleDelay = 15 * time.Second
)

// Runner is everything the recovery does to the machine. It is an interface so
// the whole ladder can be tested without suspending a laptop.
type Runner interface {
	// Status runs `wsl --status`. A timeout must be reported as
	// context.DeadlineExceeded, because a hang and a failure lead to
	// different decisions.
	Status(ctx context.Context, timeout time.Duration) error
	// Exec runs a trivial command inside a distribution.
	Exec(ctx context.Context, distro string, timeout time.Duration) error
	// DefaultDistro names the distribution to probe.
	DefaultDistro(ctx context.Context) (string, error)
	// Running lists what is up.
	Running(ctx context.Context) ([]string, error)
	// Shutdown stops the VM, forcibly when asked.
	Shutdown(ctx context.Context, force bool, timeout time.Duration) error
	// KillService kills wslservice.exe. Administrator only.
	KillService(ctx context.Context, timeout time.Duration) error
	// RestartService restarts it properly. Administrator only.
	RestartService(ctx context.Context, timeout time.Duration) error
	// GuestClock reads the clock inside a distribution.
	GuestClock(ctx context.Context, distro string, timeout time.Duration) (time.Time, error)
	// StepClock steps the guest clock from the hardware clock.
	StepClock(ctx context.Context, distro string, timeout time.Duration) error
	// Now is the host clock, injected so a test is not at the mercy of one.
	Now() time.Time
	// Sleep waits, injected so a test does not.
	Sleep(d time.Duration)
}

// Report is what one recovery run did.
type Report struct {
	Health   Health
	Decision Decision
	// Attempted are the rungs actually tried, with what happened.
	Attempted []Attempt
	// Recovered says WSL answered by the end.
	Recovered bool
	// ClockStepped says the guest clock was corrected.
	ClockStepped bool
	// Duration is how long the whole thing took.
	Duration time.Duration
}

// Attempt is one rung and its outcome.
type Attempt struct {
	Rung Rung
	Err  error
	// Healthy says the probe after this rung succeeded, which is what stops
	// the ladder.
	Healthy bool
}

// Check probes without changing anything.
func Check(ctx context.Context, r Runner, wasRunning []string, wasRunningKnown bool) Health {
	h := Health{WasRunningKnown: wasRunningKnown, WasRunning: len(wasRunning) > 0}

	err := r.Status(ctx, StatusTimeout)
	switch {
	case err == nil:
		h.ServiceAnswered = true
	case errors.Is(err, context.DeadlineExceeded):
		h.ServiceHung = true
		h.Err = err
		// A wedged service means the distribution probe would hang too, and
		// spending another twenty seconds learning that helps nobody.
		return h
	default:
		h.Err = err
		return h
	}

	distro, err := r.DefaultDistro(ctx)
	if err != nil || distro == "" {
		// No distributions is a healthy machine with nothing to check.
		h.DistroAnswered = true
		return h
	}
	h.Distro = distro

	switch err := r.Exec(ctx, distro, DistroTimeout); {
	case err == nil:
		h.DistroAnswered = true
	case errors.Is(err, context.DeadlineExceeded):
		h.DistroHung = true
		h.Err = err
	default:
		h.Err = err
	}

	if h.DistroAnswered {
		if guest, err := r.GuestClock(ctx, distro, StatusTimeout); err == nil {
			h.Skew = guest.Sub(r.Now())
			h.SkewKnown = true
		}
	}
	return h
}

// Run probes, decides, and climbs the ladder until WSL answers.
//
// Between rungs it probes again, because the point is to stop at the first one
// that worked rather than to run the whole ladder every time. The most
// disruptive step is the one nobody should reach on an ordinary morning.
func Run(ctx context.Context, r Runner, o Options, wasRunning []string, wasRunningKnown bool, log io.Writer) Report {
	start := r.Now()
	rep := Report{}
	logf := func(format string, args ...any) {
		if log != nil {
			fmt.Fprintf(log, "%s  %s\n", r.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
		}
	}

	rep.Health = Check(ctx, r, wasRunning, wasRunningKnown)
	rep.Decision = Decide(rep.Health, o)
	logf("%s", rep.Decision.Reason)

	if rep.Decision.FixClock && !o.DryRun && rep.Health.Distro != "" {
		// Done before the ladder: a clock that is hours out breaks TLS, and
		// if the ladder restarts the VM the correction has to happen again
		// anyway, which is cheap.
		if err := r.StepClock(ctx, rep.Health.Distro, StatusTimeout); err != nil {
			logf("could not step the guest clock: %v", err)
		} else {
			rep.ClockStepped = true
			logf("stepped the guest clock in %s", rep.Health.Distro)
		}
	}

	if len(rep.Decision.Ladder) == 0 {
		rep.Recovered = rep.Health.Healthy()
		rep.Duration = r.Now().Sub(start)
		return rep
	}
	if o.DryRun {
		for _, rung := range rep.Decision.Ladder {
			logf("would try: %s", rung)
		}
		rep.Duration = r.Now().Sub(start)
		return rep
	}

	for _, rung := range rep.Decision.Ladder {
		logf("trying: %s", rung)
		a := Attempt{Rung: rung, Err: climb(ctx, r, rung)}
		if a.Err != nil {
			logf("%s: %v", rung, a.Err)
		}
		// Probed even when the rung reported an error: `wsl --shutdown` can
		// time out and still have done its job, and the only thing that
		// settles it is asking.
		h := Check(ctx, r, wasRunning, wasRunningKnown)
		a.Healthy = h.Healthy()
		rep.Attempted = append(rep.Attempted, a)
		if a.Healthy {
			rep.Recovered = true
			logf("WSL answers again after %s", rung)
			break
		}
	}
	if !rep.Recovered {
		logf("WSL still does not answer after %d step(s)", len(rep.Attempted))
	}
	rep.Duration = r.Now().Sub(start)
	return rep
}

// climb takes one rung.
func climb(ctx context.Context, r Runner, rung Rung) error {
	switch rung {
	case RungShutdown:
		return r.Shutdown(ctx, false, ShutdownTimeout)
	case RungForceShutdown:
		return r.Shutdown(ctx, true, ShutdownTimeout)
	case RungKillService:
		return r.KillService(ctx, ShutdownTimeout)
	case RungRestartService:
		return r.RestartService(ctx, ShutdownTimeout)
	}
	return fmt.Errorf("guard: no such step %v", rung)
}
