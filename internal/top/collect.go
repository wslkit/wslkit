package top

import (
	"context"
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
	// Interval is how long to wait between two samples so a CPU rate can be
	// computed. Zero takes one sample and reports no CPU.
	Interval time.Duration
	// Timeout bounds each guest command.
	Timeout time.Duration
	// Only limits the measurement to these distributions.
	Only []string
}

// Collect measures every running distribution.
//
// Distributions are measured concurrently. Measuring them one after another
// would spread a CPU rate across however long the whole sweep took, which makes
// the busiest distribution look calmer the more of them there are.
func Collect(ctx context.Context, r Runner, o Options) (Report, map[string]*float64, error) {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	names, err := r.Running(ctx)
	if err != nil {
		return Report{}, nil, err
	}
	if len(o.Only) > 0 {
		names = intersect(names, o.Only)
	}
	if len(names) == 0 {
		return Report{}, nil, nil
	}

	first, vm := sweep(ctx, r, names, timeout)
	report := Report{VM: vm, Samples: first}

	if o.Interval <= 0 {
		return report, nil, nil
	}

	select {
	case <-time.After(o.Interval):
	case <-ctx.Done():
		return report, nil, ctx.Err()
	}

	second, vm2 := sweep(ctx, r, names, timeout)
	report.Samples = second
	report.Interval = o.Interval
	if vm2.TotalBytes != 0 {
		report.VM = vm2
	}

	before := map[string]Sample{}
	for _, s := range first {
		before[s.Distro] = s
	}
	rates := map[string]*float64{}
	for _, s := range second {
		if s.Err != nil {
			continue
		}
		b, ok := before[s.Distro]
		if !ok || b.Err != nil {
			continue
		}
		rates[s.Distro] = CPUPercent(b, s, o.Interval)
	}
	return report, rates, nil
}

// sweep measures every distribution at once.
func sweep(ctx context.Context, r Runner, names []string, timeout time.Duration) ([]Sample, VM) {
	type result struct {
		sample Sample
		vm     VM
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			out, err := r.Sample(ctx, name, timeout)
			if err != nil {
				results[i] = result{sample: Sample{Distro: name, Err: err}}
				return
			}
			s, vm, err := ParseSample(name, out)
			if err != nil {
				s.Err = err
			}
			results[i] = result{sample: s, vm: vm}
		}(i, name)
	}
	wg.Wait()

	samples := make([]Sample, 0, len(results))
	var vm VM
	for _, res := range results {
		samples = append(samples, res.sample)
		// Every distribution reports the same VM, since they share one
		// kernel. Take the first that answered.
		if vm.TotalBytes == 0 && res.vm.TotalBytes != 0 {
			vm = res.vm
		}
	}
	return samples, vm
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
