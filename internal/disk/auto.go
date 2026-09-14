package disk

import (
	"fmt"
	"strings"
)

// Compacting on a schedule is the natural way to run this: once a week, out of
// hours, on every disk. What makes that a bad idea in its plain form is that
// compaction has to stop the distribution, so an unattended run interrupts work
// to reclaim a few megabytes that nobody would have chosen to interrupt for.
//
// --auto is the rule that makes it worth scheduling: do the work that costs
// nothing, and stop a distribution only when there is enough to reclaim to
// justify it.

// DefaultMinReclaim is the floor for stopping a running distribution.
//
// A gigabyte is roughly the point where a person told what the interruption
// bought them would say it was worth it. Below that, the honest answer on a
// machine somebody is using is to wait for the next run.
const DefaultMinReclaim = 1 << 30

// AutoOptions is the rule --auto applies.
type AutoOptions struct {
	// MinReclaim is how much has to be reclaimable before a distribution is
	// stopped for it. Zero means the default.
	MinReclaim uint64
	// Shutdown permits stopping every distribution, which makes a target
	// whose disk the utility VM is holding eligible again.
	Shutdown bool
}

// AutoDecision is what --auto decided about one distribution, and why.
type AutoDecision struct {
	// Compact is whether to do it.
	Compact bool
	// Reason is the sentence printed next to the name either way. It is
	// written to be read in a log the morning after.
	Reason string
}

// DecideAuto applies the rule to one distribution.
//
// disrupts says whether compacting this one would have to stop something: the
// distribution itself, or every distribution when the utility VM is holding the
// disk open on behalf of another.
//
// The two cases are genuinely different. Compacting a stopped distribution on a
// machine where the VM is down interrupts nobody, so there is nothing to weigh
// against it and it goes ahead whatever the estimate says — which matters,
// because the estimate for a stopped distribution cannot be read without
// starting it, and starting it is exactly the disruption being avoided.
func DecideAuto(info Info, disrupts bool, o AutoOptions) AutoDecision {
	min := o.MinReclaim
	if min == 0 {
		min = DefaultMinReclaim
	}
	if !disrupts {
		return AutoDecision{Compact: true, Reason: "nothing has to be stopped for it"}
	}
	rec := info.Reclaimable()
	if rec == nil {
		// The guest's own figure is the only honest source, and it can only
		// be read from a distribution that is already running.
		return AutoDecision{Reason: "skipped: how much is reclaimable cannot be read without starting it"}
	}
	if *rec < min {
		return AutoDecision{Reason: fmt.Sprintf("skipped: %s reclaimable is below the %s worth stopping it for",
			FormatSize(*rec), FormatSize(min))}
	}
	return AutoDecision{Compact: true, Reason: fmt.Sprintf("%s reclaimable", FormatSize(*rec))}
}

// ParseSize reads a size written the way this tool prints them.
//
// FormatSize writes "1.5 GiB", and .wslconfig writes "1GB" for the same
// quantity, so both spellings are accepted and both mean powers of two. A bare
// number is bytes.
func ParseSize(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	lower := strings.ToLower(s)
	mult := uint64(1)
	for _, suf := range []struct {
		s string
		m uint64
	}{
		{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10},
		{"tb", 1 << 40}, {"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"b", 1},
	} {
		if strings.HasSuffix(lower, suf.s) {
			lower = strings.TrimSuffix(lower, suf.s)
			mult = suf.m
			break
		}
	}
	lower = strings.TrimSpace(lower)
	if lower == "" {
		return 0, false
	}
	// A fraction is how a size gets read back off this tool's own output.
	whole, frac, hasFrac := strings.Cut(lower, ".")
	n, ok := digits(whole)
	if !ok {
		return 0, false
	}
	total := n * mult
	if hasFrac {
		f, ok := digits(frac)
		if !ok {
			return 0, false
		}
		scale := uint64(1)
		for range frac {
			scale *= 10
		}
		total += f * mult / scale
	}
	return total, true
}

func digits(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + uint64(r-'0')
	}
	return n, true
}
