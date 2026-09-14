//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/disk"
)

// ---------------------------------------------------------------- trim

func (a *App) diskTrim(args []string) int {
	fs := flag.NewFlagSet("disk trim", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	trimTimeout := fs.Duration("trim-timeout", disk.DefaultTrimTimeout, "how long to let fstrim run")
	name, code := oneDistroArg(a, fs, args, "disk trim")
	if code != ExitOK {
		return code
	}

	e := diskEnv()
	list, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	r, err := disk.Resolve(list, name)
	if err != nil {
		return a.diskFail(f, err)
	}

	plan, err := disk.PlanTrim(r)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	res, err := disk.Trim(context.Background(), e, r, *trimTimeout)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.TrimJSON(res)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	disk.RenderTrim(a.Stdout, res)
	return ExitOK
}

// ---------------------------------------------------------------- compact

func (a *App) diskCompact(args []string) int {
	fs := flag.NewFlagSet("disk compact", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	all := fs.Bool("all", false, "compact every WSL 2 distribution")
	file := fs.String("file", "", "compact a loose .vhdx instead of the disk of a distribution")
	orphans := fs.Bool("orphans", false, "compact the loose disks that no distribution claims")
	auto := fs.Bool("auto", false, "with --all, skip the disks not worth stopping a distribution for")
	minReclaim := fs.String("min-reclaim", "", "with --auto, how much must be reclaimable to stop a distribution (default 1GiB)")
	var scan stringList
	fs.Var(&scan, "scan", "another directory to search for loose disks; may be given more than once")
	noTrim := fs.Bool("no-trim", false, "skip the fstrim step")
	restart := fs.Bool("restart", false, "start the distribution again afterwards if it was running")
	shutdown := fs.Bool("shutdown", false, "permit stopping every distribution to free the disk")
	unlock := fs.Duration("unlock-timeout", disk.DefaultUnlockTimeout, "how long to wait for the utility VM to release the disk")
	trimTimeout := fs.Duration("trim-timeout", disk.DefaultTrimTimeout, "how long to let fstrim run")

	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	extra := fs.Args()
	if name == "" && len(extra) > 0 {
		name, extra = extra[0], extra[1:]
	}
	if len(extra) > 0 {
		fmt.Fprintf(a.Stderr, "disk compact takes one distribution name, got %q as well\n", extra[0])
		return ExitUsage
	}

	// Exactly one target. Naming none leaves nothing to do, and naming two
	// means the caller expected something the command cannot deliver.
	named := 0
	for _, on := range []bool{name != "", *all, *file != "", *orphans} {
		if on {
			named++
		}
	}
	if named != 1 {
		fmt.Fprintln(a.Stderr, "name one distribution, --all, --orphans, or --file")
		fmt.Fprintln(a.Stderr, `for example: wslkit disk compact Ubuntu, wslkit disk compact --all, or wslkit disk compact --file D:\disks\data.vhdx`)
		return ExitUsage
	}
	if len(scan) > 0 && !*orphans {
		fmt.Fprintln(a.Stderr, "--scan only means something with --orphans, which is what searches for disks")
		return ExitUsage
	}
	if (*auto || *minReclaim != "") && !*all {
		fmt.Fprintln(a.Stderr, "--auto goes with --all: it is the rule for which of several disks are worth compacting")
		return ExitUsage
	}
	var minBytes uint64
	if *minReclaim != "" {
		n, ok := disk.ParseSize(*minReclaim)
		if !ok {
			fmt.Fprintf(a.Stderr, "--min-reclaim %q is not a size: use 500MB, 2GiB, or a number of bytes\n", *minReclaim)
			return ExitUsage
		}
		minBytes = n
	}

	ctx := context.Background()
	e := diskEnv()

	// Settings supply the defaults; a flag actually given on the command line
	// wins, in either direction.
	cfg := a.diskConfigOrDefaults(e)
	o := disk.CompactOptions{
		Trim:          cfg.CompactTrim,
		Shutdown:      *shutdown,
		Restart:       cfg.CompactRestart,
		UnlockTimeout: time.Duration(cfg.UnlockTimeoutSeconds) * time.Second,
		TrimTimeout:   *trimTimeout,
	}
	if isSet(fs, "no-trim") {
		o.Trim = !*noTrim
	}
	if isSet(fs, "restart") {
		o.Restart = *restart
	}
	if isSet(fs, "unlock-timeout") {
		o.UnlockTimeout = *unlock
	}

	if *orphans {
		targets, code := a.orphanTargets(ctx, f, e, scan, &o)
		if code != ExitOK || len(targets) == 0 {
			return code
		}
		return a.runCompaction(ctx, f, e, targets, o, false)
	}

	targets, err := a.compactTargets(ctx, e, name, *all, *file, &o)
	if err != nil {
		return a.diskFail(f, err)
	}
	if *auto {
		targets = a.autoFilter(ctx, f, e, targets, o, disk.AutoOptions{MinReclaim: minBytes, Shutdown: o.Shutdown})
		if len(targets) == 0 {
			// autoFilter has already said why, once per distribution.
			// Nothing worth doing is a successful run, which is what a
			// scheduled task needs it to be.
			return ExitOK
		}
	}
	if len(targets) == 0 {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "no WSL 2 distributions to compact")
		}
		return ExitOK
	}
	return a.runCompaction(ctx, f, e, targets, o, *all)
}

// runCompaction plans and runs every target, reporting each. A refusal on one
// target does not stop the rest: with several disks named, the ones that can be
// compacted should be.
func (a *App) runCompaction(ctx context.Context, f diskFlags, e disk.Env, targets []disk.CompactTarget, o disk.CompactOptions, label bool) int {

	// Progress on stdout would corrupt the JSON stream, so it is rendered to
	// stderr and only for a human.
	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}

	exit := ExitOK
	var total uint64
	var results []disk.CompactResult
	for _, t := range targets {
		plan, err := disk.PlanCompact(e, t, o)
		if err != nil {
			// A refusal is reported against its target, and under --all
			// the run continues to the disks that can be compacted.
			results = append(results, disk.CompactResult{Label: t.Label(), Err: err})
			if exit == ExitOK {
				exit = diskExitFor(err)
			}
			continue
		}
		if err := plan.Valid(); err != nil {
			return a.diskFail(f, err)
		}
		if f.dryRun {
			prefix := ""
			if label {
				prefix = t.Label() + ":\n"
			}
			if code := a.renderPlanPrefixed(f, plan, prefix); code != ExitOK {
				return code
			}
			continue
		}

		res := disk.Compact(ctx, e, t, o, progress)
		results = append(results, res)
		if res.Err != nil && exit == ExitOK {
			exit = diskExitFor(res.Err)
		}
		if r := res.Reclaimed(); r != nil {
			total += *r
		}
	}
	if f.dryRun {
		return ExitOK
	}

	for _, res := range results {
		if f.jsonOut {
			if err := disk.WriteJSONLine(a.Stdout, disk.CompactJSON(res)); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			continue
		}
		if res.Err != nil {
			fmt.Fprintf(a.Stderr, "%s: %v\n", res.Label, res.Err)
			continue
		}
		disk.RenderCompact(a.Stdout, res)
	}
	if !f.jsonOut && len(results) > 1 {
		fmt.Fprintf(a.Stdout, "\n%s reclaimed in total\n", disk.FormatSize(total))
	}
	return exit
}

// autoFilter keeps the targets worth compacting, and says why it dropped the
// rest.
//
// The rule is in the disk package; what is here is working out which targets
// compacting would actually disrupt. A distribution that is running has to be
// stopped. So does every distribution, if the utility VM is up and holding the
// disk open on behalf of another one — which is the case that makes an
// unattended run interrupt somebody who was not using the disk being compacted.
func (a *App) autoFilter(ctx context.Context, f diskFlags, e disk.Env, targets []disk.CompactTarget, o disk.CompactOptions, ao disk.AutoOptions) []disk.CompactTarget {
	vmUp := len(o.RunningBefore) > 0

	var keep []disk.CompactTarget
	for _, t := range targets {
		if t.Reg.Name == "" {
			// A loose file has no distribution to stop.
			keep = append(keep, t)
			continue
		}
		running := o.RunningBefore[t.Reg.Name]
		// Stopping this one is enough when it is the only one up; otherwise
		// the VM holds the disk and freeing it means stopping them all.
		disrupts := running || vmUp

		info := disk.Info{}
		if running {
			// Only readable for a distribution that is already running,
			// which is exactly the case where the answer decides
			// something.
			info = disk.Measure(ctx, e, t.Reg, disk.MeasureOptions{Running: o.RunningBefore, Timeout: f.timeout})
		}
		d := disk.DecideAuto(info, disrupts, ao)
		if !f.jsonOut {
			fmt.Fprintf(a.Stdout, "%-24s %s\n", t.Reg.Name, d.Reason)
		} else if !d.Compact {
			// A skip leaves no other trace in the JSON stream, and a
			// scheduled run wants a record of what it decided.
			_ = disk.WriteJSONLine(a.Stdout, map[string]any{
				"distribution": t.Reg.Name, "compacted": false, "skipped": true, "reason": d.Reason,
			})
		}
		if d.Compact {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 && !f.jsonOut {
		fmt.Fprintln(a.Stdout, "\nnothing was worth compacting")
	} else if !f.jsonOut {
		fmt.Fprintln(a.Stdout)
	}
	return keep
}

// orphanTargets scans for loose disks and turns them into compaction targets.
//
// `disk orphans` already finds them and `--file` already compacts one; joining
// the two is what lets somebody reclaim the space Docker Desktop's disk is
// sitting on without copying a path out of one command and into another.
//
// Compacting is not deleting, so this does not carry the warning that
// `orphans --delete` does. It does say whose disk each one is before touching
// it, because a disk another application is using may have to be stopped first,
// and because a user is entitled to know what they are about to rewrite.
func (a *App) orphanTargets(ctx context.Context, f diskFlags, e disk.Env, scan []string, o *disk.CompactOptions) ([]disk.CompactTarget, int) {
	list, warnings, err := e.Registry.Distros()
	if err != nil {
		return nil, a.diskFail(f, err)
	}
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	cfg := a.diskConfigOrDefaults(e)
	roots := append(append([]string{}, cfg.ScanDirs...), scan...)
	found, scanWarnings, err := disk.ScanOrphans(e, list, roots)
	if err != nil {
		return nil, a.diskFail(f, err)
	}
	for _, w := range scanWarnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}
	if len(found) == 0 {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "no orphaned disks found")
		}
		return nil, ExitOK
	}

	// Same snapshot as the registered path: with --shutdown the first target
	// stops everything, and a later target would otherwise look as though
	// nothing had been running.
	o.RunningBefore = map[string]bool{}
	if names, err := e.Host.Running(ctx); err == nil {
		for _, n := range names {
			o.RunningBefore[n] = true
		}
	}

	if !f.jsonOut {
		disk.RenderOrphans(a.Stdout, found)
		fmt.Fprintln(a.Stdout)
	}
	// A dry run prints the plan for each disk further down, and never asks.
	if !f.dryRun && !f.yes {
		if f.jsonOut {
			// Nothing can answer a prompt in the middle of a JSON stream.
			fmt.Fprintln(a.Stderr, "--json with --orphans needs -y: these disks belong to other software, so the command will not compact them without being told to")
			return nil, ExitUsage
		}
		if !a.confirm(fmt.Sprintf("compact %d disk(s) that no distribution claims?", len(found))) {
			fmt.Fprintln(a.Stdout, "nothing was compacted")
			return nil, ExitOK
		}
	}

	var targets []disk.CompactTarget
	for _, orphan := range found {
		targets = append(targets, disk.CompactTarget{Path: orphan.Path})
	}
	return targets, ExitOK
}

// compactTargets resolves what to compact and records which distributions were
// up before anything is stopped.
func (a *App) compactTargets(ctx context.Context, e disk.Env, name string, all bool, file string, o *disk.CompactOptions) ([]disk.CompactTarget, error) {
	if file != "" {
		return []disk.CompactTarget{{Path: file}}, nil
	}
	list, warnings, err := e.Registry.Distros()
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	// Taken once, before the first target stops anything. With --shutdown the
	// first target stops them all, so asking again later would report every
	// remaining distribution as never having been up.
	o.RunningBefore = map[string]bool{}
	if names, err := e.Host.Running(ctx); err == nil {
		for _, n := range names {
			o.RunningBefore[n] = true
		}
	}

	if !all {
		r, err := disk.Resolve(list, name)
		if err != nil {
			return nil, err
		}
		return []disk.CompactTarget{{Reg: r}}, nil
	}
	var out []disk.CompactTarget
	for _, r := range disk.Sorted(list) {
		if r.Version != 2 {
			// Nothing to compact, and saying so once per WSL 1
			// distribution would be noise.
			continue
		}
		out = append(out, disk.CompactTarget{Reg: r})
	}
	return out, nil
}

// ---------------------------------------------------------------- shared

// oneDistroArg parses a subcommand that takes exactly one distribution name,
// written before or after the flags.
func oneDistroArg(a *App, fs *flag.FlagSet, args []string, command string) (string, int) {
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", ExitUsage
	}
	extra := fs.Args()
	if name == "" && len(extra) > 0 {
		name, extra = extra[0], extra[1:]
	}
	if len(extra) > 0 {
		fmt.Fprintf(a.Stderr, "%s takes one distribution name, got %q as well\n", command, extra[0])
		return "", ExitUsage
	}
	if name == "" {
		fmt.Fprintf(a.Stderr, "%s needs the name of a distribution\n", command)
		return "", ExitUsage
	}
	return name, ExitOK
}

// renderPlan answers --dry-run with the plan.
func (a *App) renderPlan(f diskFlags, p disk.Plan) int {
	return a.renderPlanPrefixed(f, p, "")
}

func (a *App) renderPlanPrefixed(f diskFlags, p disk.Plan, prefix string) int {
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.DryRunJSON(p)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	disk.RenderDryRun(a.Stdout, p, prefix)
	return ExitOK
}

// ---------------------------------------------------------------- usage

func (a *App) diskUsageCmd(args []string) int {
	fs := flag.NewFlagSet("disk usage", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	top := fs.Int("top", 0, "show only the largest N entries")
	byDir := fs.Bool("by-directory", false, "also break the whole guest down by directory")
	depth := fs.Int("depth", disk.DefaultDepth, "how deep the directory breakdown goes")
	name, code := oneDistroArg(a, fs, args, "disk usage")
	if code != ExitOK {
		return code
	}
	if *depth < 1 || *depth > disk.MaxDepth {
		fmt.Fprintf(a.Stderr, "--depth must be between 1 and %d\n", disk.MaxDepth)
		return ExitUsage
	}
	if !*byDir && isSet(fs, "depth") {
		fmt.Fprintln(a.Stderr, "--depth only means something with --by-directory")
		return ExitUsage
	}
	if *top < 0 {
		fmt.Fprintln(a.Stderr, "--top must not be negative")
		return ExitUsage
	}

	e := diskEnv()
	list, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	r, err := disk.Resolve(list, name)
	if err != nil {
		return a.diskFail(f, err)
	}
	plan, err := disk.PlanUsage(r)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	o := disk.UsageOptions{Top: *top, ByDirectory: *byDir, Depth: *depth, Timeout: disk.DefaultUsageTimeout}
	u, err := disk.MeasureUsage(context.Background(), e, r, o)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.UsageJSON(u)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	disk.RenderUsage(a.Stdout, u)
	return ExitOK
}

// isSet reports whether a flag was given on the command line, as opposed to
// holding its default. The flag package only exposes this by walking the set.
func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == name {
			found = true
		}
	})
	return found
}
