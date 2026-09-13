//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

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
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	r, err := disk.Resolve(list, name)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}

	plan, err := disk.PlanTrim(r)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	res, err := disk.Trim(context.Background(), e, r, *trimTimeout)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
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
	for _, on := range []bool{name != "", *all, *file != ""} {
		if on {
			named++
		}
	}
	if named != 1 {
		fmt.Fprintln(a.Stderr, "name one distribution, --all, or --file")
		fmt.Fprintln(a.Stderr, `for example: wslkit disk compact Ubuntu, wslkit disk compact --all, or wslkit disk compact --file D:\disks\data.vhdx`)
		return ExitUsage
	}

	o := disk.CompactOptions{
		Trim:          !*noTrim,
		Shutdown:      *shutdown,
		Restart:       *restart,
		UnlockTimeout: *unlock,
		TrimTimeout:   *trimTimeout,
	}

	ctx := context.Background()
	e := diskEnv()

	targets, err := a.compactTargets(ctx, e, name, *all, *file, &o)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if len(targets) == 0 {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "no WSL 2 distributions to compact")
		}
		return ExitOK
	}

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
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		if f.dryRun {
			prefix := ""
			if *all {
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
