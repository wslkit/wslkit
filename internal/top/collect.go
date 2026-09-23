package top

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds one measurement of one distribution. Walking /proc on a
// busy distribution takes a moment, and one that is shutting down may not
// answer at all.
const DefaultTimeout = 30 * time.Second

// Runner executes the sample script inside a distribution. It is an interface
// so the collector can be exercised without WSL, which is what lets the
// concurrency and the rate arithmetic be tested at all.
type Runner interface {
	Running(ctx context.Context) ([]string, error)
	Sample(ctx context.Context, distro string, timeout time.Duration) (string, error)
}

// Options controls a measurement.
type Options struct {
	// Interval is how long to wait between two samples so a rate can be
	// computed. Zero takes one sample and reports no rates.
	Interval time.Duration
	// Timeout bounds each guest command.
	Timeout time.Duration
	// Only limits the measurement to these distributions.
	Only []string
}

// Collect measures every running distribution, twice when an interval is
// asked for, and reports what changed in between.
func Collect(ctx context.Context, r Runner, o Options) (Report, error) {
	first, err := Sweep(ctx, r, o)
	if err != nil || o.Interval <= 0 || (len(first.Samples) == 0 && len(first.Sessions) == 0) {
		return first, err
	}

	select {
	case <-time.After(o.Interval):
	case <-ctx.Done():
		return first, ctx.Err()
	}

	second, err := Sweep(ctx, r, o)
	if err != nil {
		return first, err
	}
	if second.VM.TotalBytes == 0 {
		second.VM = first.VM
	}
	return WithRates(first, second, o.Interval), nil
}

// Sweep measures every running distribution once, and every wslc session
// when asked to.
//
// Everything is measured concurrently. Measuring one after another would
// spread a rate across however long the whole sweep took, which makes the
// busiest distribution look calmer the more of them there are.
func Sweep(ctx context.Context, r Runner, o Options) (Report, error) {
	started := time.Now()
	timeout := o.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	names, err := r.Running(ctx)
	if err != nil {
		return Report{}, err
	}
	if len(o.Only) > 0 {
		names = intersect(names, o.Only)
	}

	// Sessions are separate VMs, so they are read alongside the
	// distributions rather than after them.
	type sessionResult struct {
		sessions []Session
		err      error
	}
	sessionsDone := make(chan sessionResult, 1)
	sr, canSessions := r.(SessionReader)
	if canSessions {
		go func() {
			s, err := sweepSessions(ctx, sr, timeout, started)
			sessionsDone <- sessionResult{s, err}
		}()
	}

	type result struct {
		sample Sample
		vm     VM
		groups []Sample
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			out, err := r.Sample(ctx, name, timeout)
			if err != nil {
				results[i] = result{sample: Sample{Distro: name, Kind: KindDistro, Err: err}}
				return
			}
			s, vm, err := ParseSample(name, out)
			if err != nil {
				s.Err = err
				results[i] = result{sample: s}
				return
			}
			results[i] = result{sample: s, vm: vm, groups: ParseGroups(out)}
		}(i, name)
	}
	wg.Wait()

	report := Report{SampledAt: time.Now(), StartedAt: started}
	for _, res := range results {
		report.Samples = append(report.Samples, res.sample)
		// Every distribution reports the same VM and the same groups, since
		// they share one kernel. Take the first that answered.
		if report.VM.TotalBytes == 0 && res.vm.TotalBytes != 0 {
			report.VM = res.vm
			report.Groups = res.groups
		}
	}
	if canSessions {
		// Only sessions whose VM is already running come back, so a machine
		// without wslc, or with every session stopped, gets no section at all.
		res := <-sessionsDone
		if len(res.sessions) > 0 {
			report.Sessions = res.sessions
		}
		if res.err != nil {
			report.Notes = append(report.Notes, fmt.Sprintf("wslc sessions could not be listed: %v", res.err))
		}
	}
	readHost(ctx, r, &report)
	return report, nil
}

func intersect(have, want []string) []string {
	var out []string
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}
