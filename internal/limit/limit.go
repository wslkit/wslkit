// Package limit caps what one distribution may use.
//
// WSL caps the whole utility VM, in .wslconfig, and nothing else. One
// distribution running a runaway build takes memory from every other one and
// from Windows, and there is no setting anywhere that says "this distribution
// gets four gigabytes".
//
// From WSL 2.9 there is somewhere to put one. mini_init creates a cgroup per
// distribution under /sys/fs/cgroup/wsl-user/distro-<pid>, and the ordinary
// cgroup v2 controls in it work: memory.max, memory.high and cpu.max are
// exactly the knobs this needs, already mounted, already writable by root
// inside the distribution.
//
// Two things follow from where that node lives, and both shape the whole
// design. It is named after the distribution's init pid, so it is a different
// node after every restart and a limit written into it does not survive one.
// And it is above the distribution's own view of itself, so the writing has to
// happen from inside, as root, against a path resolved at that moment.
package limit

import (
	"fmt"
	"strconv"
	"strings"
)

// Limits is what to apply. A zero field means "leave whatever is there".
type Limits struct {
	// MemoryMax is the hard ceiling. A process that would take the
	// distribution past it is killed by the kernel's OOM killer, inside
	// that distribution, with no warning to anything outside.
	MemoryMax uint64
	// MemoryHigh is the throttle. Past it the kernel reclaims aggressively
	// and slows the distribution down rather than killing anything, which is
	// almost always what somebody actually wants.
	MemoryHigh uint64
	// CPUs is how many processors' worth of time the distribution may use.
	// Fractional, so 1.5 is a processor and a half.
	CPUs float64
	// Swap caps swap separately. Zero leaves it alone; SwapOff turns it off.
	Swap    uint64
	SwapOff bool
}

// Empty reports whether there is anything to do.
func (l Limits) Empty() bool {
	return l.MemoryMax == 0 && l.MemoryHigh == 0 && l.CPUs == 0 && l.Swap == 0 && !l.SwapOff
}

// CPUPeriod is the accounting window cpu.max uses. 100 ms is the kernel's
// default and there is no reason to differ: a shorter one throttles in smaller,
// more visible steps, a longer one lets a burst run further before it is
// stopped.
const CPUPeriod = 100000

// Write is one file to write, and why.
type Write struct {
	// File is the name inside the distribution's cgroup directory.
	File string
	// Value is what goes in it.
	Value string
	// Why is one line for the dry run.
	Why string
}

// Plan renders the writes for these limits.
//
// The order matters: memory.high before memory.max, so that a distribution
// which is already over the new ceiling starts being throttled before it starts
// being killed. Writing max first would OOM-kill whatever was running in the
// gap.
func Plan(l Limits) []Write {
	var out []Write
	if l.MemoryHigh > 0 {
		out = append(out, Write{
			File:  "memory.high",
			Value: strconv.FormatUint(l.MemoryHigh, 10),
			Why:   "throttle: past this the kernel reclaims hard and the distribution slows down",
		})
	}
	if l.MemoryMax > 0 {
		out = append(out, Write{
			File:  "memory.max",
			Value: strconv.FormatUint(l.MemoryMax, 10),
			Why:   "ceiling: past this the kernel kills a process inside the distribution",
		})
	}
	switch {
	case l.SwapOff:
		out = append(out, Write{File: "memory.swap.max", Value: "0", Why: "no swap for this distribution"})
	case l.Swap > 0:
		out = append(out, Write{
			File:  "memory.swap.max",
			Value: strconv.FormatUint(l.Swap, 10),
			Why:   "swap ceiling",
		})
	}
	if l.CPUs > 0 {
		quota := int64(l.CPUs * float64(CPUPeriod))
		if quota < 1000 {
			// Below a millisecond per period the distribution cannot make
			// progress at all, and a limit that hangs a machine is worse
			// than no limit.
			quota = 1000
		}
		out = append(out, Write{
			File:  "cpu.max",
			Value: fmt.Sprintf("%d %d", quota, CPUPeriod),
			Why:   fmt.Sprintf("%s of processor time per %dms window", cpuText(l.CPUs), CPUPeriod/1000),
		})
	}
	return out
}

// Clear renders the writes that remove every limit.
//
// "max" is the cgroup v2 way of saying unlimited, for all three; cpu.max takes
// it as the quota with the period unchanged.
func Clear() []Write {
	return []Write{
		{File: "memory.max", Value: "max", Why: "no ceiling"},
		{File: "memory.high", Value: "max", Why: "no throttle"},
		{File: "memory.swap.max", Value: "max", Why: "no swap ceiling"},
		{File: "cpu.max", Value: fmt.Sprintf("max %d", CPUPeriod), Why: "no processor limit"},
	}
}

// Current is what a distribution's cgroup says right now.
type Current struct {
	// Node is the cgroup path inside the distribution, for the report.
	Node string
	// MemoryMax and the rest are the raw values, with "max" preserved
	// rather than turned into a number: "max" and "9223372036854771712" mean
	// the same thing to the kernel and different things to a reader.
	MemoryMax  string
	MemoryHigh string
	SwapMax    string
	CPUMax     string
	// MemoryCurrent is what it is using now, which is what decides whether a
	// new ceiling is about to kill something.
	MemoryCurrent uint64
	// Systemd says the distribution runs systemd, which puts its processes
	// in a child of the distro node.
	Systemd bool
}

// Unlimited reports whether a raw cgroup value means "no limit".
func Unlimited(v string) bool { return v == "" || v == "max" }

// FormatLimit renders a raw cgroup value for a human.
func FormatLimit(v string) string {
	if Unlimited(v) {
		return "unlimited"
	}
	if n, err := strconv.ParseUint(v, 10, 64); err == nil {
		return FormatBytes(n)
	}
	return v
}

// FormatCPUMax renders cpu.max, which is a quota and a period.
func FormatCPUMax(v string) string {
	fields := strings.Fields(v)
	if len(fields) == 0 || fields[0] == "max" {
		return "unlimited"
	}
	quota, err1 := strconv.ParseInt(fields[0], 10, 64)
	period := int64(CPUPeriod)
	if len(fields) > 1 {
		if p, err := strconv.ParseInt(fields[1], 10, 64); err == nil && p > 0 {
			period = p
		}
	}
	if err1 != nil || period == 0 {
		return v
	}
	return cpuText(float64(quota) / float64(period))
}

func cpuText(cpus float64) string {
	if cpus == float64(int64(cpus)) {
		if int64(cpus) == 1 {
			return "1 processor"
		}
		return fmt.Sprintf("%d processors", int64(cpus))
	}
	return fmt.Sprintf("%.2g processors", cpus)
}

// FormatBytes renders a byte count the way the rest of the tool does.
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// CheckAgainstUsage refuses a ceiling the distribution is already past.
//
// Writing memory.max below what a distribution is using does not fail: the
// kernel accepts it and then kills processes until the figure fits. A tool that
// does that on a typo, without saying so first, is a tool that loses somebody's
// work.
func (l Limits) CheckAgainstUsage(c Current) error {
	if l.MemoryMax == 0 || c.MemoryCurrent == 0 {
		return nil
	}
	if l.MemoryMax < c.MemoryCurrent {
		return fmt.Errorf("%s is using %s now, and a ceiling of %s would have the kernel start killing processes inside it immediately. Raise the limit, or free some memory first",
			c.Node, FormatBytes(c.MemoryCurrent), FormatBytes(l.MemoryMax))
	}
	return nil
}
