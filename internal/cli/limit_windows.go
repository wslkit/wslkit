//go:build windows

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/wslkit/wslkit/internal/disk"
	"github.com/wslkit/wslkit/internal/limit"
	"github.com/wslkit/wslkit/internal/proxy"
)

func (a *App) limitUsage() {
	fmt.Fprint(a.Stderr, `wslkit limit: cap what one distribution may use.

  wslkit limit show [-d <distro>]              what each distribution is capped at
  wslkit limit set -d <distro> [--memory 4GB]  cap it
  wslkit limit clear -d <distro>               remove the caps

set flags:
  --memory SIZE    hard ceiling: past this the kernel kills a process inside
                   the distribution
  --high SIZE      throttle: past this the kernel reclaims hard and the
                   distribution slows down. Usually the one you want
  --cpus N         processors' worth of time, fractional allowed
  --swap SIZE      swap ceiling, or --no-swap for none
  --dry-run        show what would be written and change nothing

WSL caps the whole utility VM in .wslconfig and nothing else, so one
distribution running a runaway build takes memory from every other one. From
WSL 2.9 each distribution has its own cgroup, and these are the ordinary cgroup
v2 controls in it.

The limits do not survive a restart of the distribution: the cgroup is named
after its init process and a new one is made each time it starts. wslkit limit
set again afterwards, or see the guide for a boot.command that does it.
`)
}

func (a *App) limit(args []string) int {
	if len(args) == 0 {
		a.limitUsage()
		return ExitUsage
	}
	switch args[0] {
	case "show":
		return a.limitShow(args[1:])
	case "set":
		return a.limitSet(args[1:])
	case "clear":
		return a.limitClear(args[1:])
	case "help", "--help", "-h":
		a.limitUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown limit subcommand %q\n\n", args[0])
		a.limitUsage()
		return ExitUsage
	}
}

// runningDistros is the set worth asking about: a stopped distribution has no
// cgroup, because the cgroup is made when it starts.
func (a *App) runningDistros(ctx context.Context) ([]string, error) {
	e := diskEnv()
	return e.Host.Running(ctx)
}

func (a *App) limitShow(args []string) int {
	fs := flag.NewFlagSet("limit show", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "one distribution instead of every running one")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	timeout := fs.Duration("timeout", limit.DefaultTimeout, "bound on each command run inside a distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	ctx := context.Background()
	names := []string{*distro}
	if *distro == "" {
		var err error
		names, err = a.runningDistros(ctx)
		if err != nil {
			fmt.Fprintf(a.Stderr, "%v\n", err)
			return ExitCollector
		}
		if len(names) == 0 {
			fmt.Fprintln(a.Stdout, "no distributions are running, and a stopped one has no cgroup to read")
			return ExitOK
		}
	}

	r := proxy.WSLRunner{}
	rows := 0
	for _, name := range names {
		c, err := limit.Read(ctx, r, name, *timeout)
		if err != nil {
			var none limit.ErrNoCgroup
			if errors.As(err, &none) {
				fmt.Fprintf(a.Stderr, "%v\n", err)
				return ExitCollector
			}
			fmt.Fprintf(a.Stderr, "%s: %v\n", name, err)
			continue
		}
		rows++
		if *jsonOut {
			if err := disk.WriteJSONLine(a.Stdout, map[string]any{
				"distribution":   name,
				"cgroup":         c.Node,
				"memory_max":     c.MemoryMax,
				"memory_high":    c.MemoryHigh,
				"swap_max":       c.SwapMax,
				"cpu_max":        c.CPUMax,
				"memory_current": c.MemoryCurrent,
			}); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			continue
		}
		fmt.Fprintf(a.Stdout, "%s  (%s)\n", name, c.Node)
		fmt.Fprintf(a.Stdout, "  using now   %s\n", limit.FormatBytes(c.MemoryCurrent))
		fmt.Fprintf(a.Stdout, "  ceiling     %s\n", limit.FormatLimit(c.MemoryMax))
		fmt.Fprintf(a.Stdout, "  throttle    %s\n", limit.FormatLimit(c.MemoryHigh))
		fmt.Fprintf(a.Stdout, "  swap        %s\n", limit.FormatLimit(c.SwapMax))
		fmt.Fprintf(a.Stdout, "  processors  %s\n", limit.FormatCPUMax(c.CPUMax))
	}
	if rows == 0 {
		return ExitFindings
	}
	return ExitOK
}

func (a *App) limitSet(args []string) int {
	fs := flag.NewFlagSet("limit set", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	memory := fs.String("memory", "", "hard ceiling, for example 4GB")
	high := fs.String("high", "", "throttle, for example 3GB")
	swap := fs.String("swap", "", "swap ceiling")
	noSwap := fs.Bool("no-swap", false, "no swap at all")
	cpus := fs.Float64("cpus", 0, "processors' worth of time")
	dryRun := fs.Bool("dry-run", false, "show what would be written and change nothing")
	timeout := fs.Duration("timeout", limit.DefaultTimeout, "bound on each command run inside the distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *distro == "" || fs.NArg() > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit limit set -d <distro> [--memory 4GB] [--high 3GB] [--cpus 2]")
		return ExitUsage
	}

	var l limit.Limits
	for _, spec := range []struct {
		flag  string
		value string
		into  *uint64
	}{{"--memory", *memory, &l.MemoryMax}, {"--high", *high, &l.MemoryHigh}, {"--swap", *swap, &l.Swap}} {
		if spec.value == "" {
			continue
		}
		n, ok := disk.ParseSize(spec.value)
		if !ok || n == 0 {
			fmt.Fprintf(a.Stderr, "%s %q is not a size: use 512MB, 4GiB, or a number of bytes\n", spec.flag, spec.value)
			return ExitUsage
		}
		*spec.into = n
	}
	l.CPUs = *cpus
	l.SwapOff = *noSwap
	if *noSwap && *swap != "" {
		fmt.Fprintln(a.Stderr, "--swap and --no-swap contradict each other")
		return ExitUsage
	}
	if l.Empty() {
		fmt.Fprintln(a.Stderr, "nothing to set: name at least one of --memory, --high, --cpus, --swap or --no-swap")
		return ExitUsage
	}

	ctx := context.Background()
	r := proxy.WSLRunner{}
	c, err := limit.Read(ctx, r, *distro, *timeout)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	// The check that stops a typo from killing somebody's build.
	if err := l.CheckAgainstUsage(c); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}

	writes := limit.Plan(l)
	fmt.Fprintf(a.Stdout, "%s  (%s), using %s now\n", *distro, c.Node, limit.FormatBytes(c.MemoryCurrent))
	for _, w := range writes {
		fmt.Fprintf(a.Stdout, "  %-16s %-24s %s\n", w.File, w.Value, w.Why)
	}
	if *dryRun {
		fmt.Fprintln(a.Stdout, "\n--dry-run: nothing was written")
		return ExitOK
	}
	if err := limit.Apply(ctx, r, *distro, writes, *timeout); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	fmt.Fprintf(a.Stdout, "\napplied. These go when %s next restarts: the cgroup is named after its\ninit process and a new one is made each time it starts.\n", *distro)
	return ExitOK
}

func (a *App) limitClear(args []string) int {
	fs := flag.NewFlagSet("limit clear", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	dryRun := fs.Bool("dry-run", false, "show what would be written and change nothing")
	timeout := fs.Duration("timeout", limit.DefaultTimeout, "bound on each command run inside the distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *distro == "" || fs.NArg() > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit limit clear -d <distro>")
		return ExitUsage
	}

	ctx := context.Background()
	r := proxy.WSLRunner{}
	c, err := limit.Read(ctx, r, *distro, *timeout)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	writes := limit.Clear()
	fmt.Fprintf(a.Stdout, "%s  (%s)\n", *distro, c.Node)
	for _, w := range writes {
		fmt.Fprintf(a.Stdout, "  %-16s %-24s %s\n", w.File, w.Value, w.Why)
	}
	if *dryRun {
		fmt.Fprintln(a.Stdout, "\n--dry-run: nothing was written")
		return ExitOK
	}
	if err := limit.Apply(ctx, r, *distro, writes, *timeout); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	fmt.Fprintln(a.Stdout, "\ncleared")
	return ExitOK
}
