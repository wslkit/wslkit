//go:build windows

package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
)

// stringList collects a flag given more than once.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ", ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// ---------------------------------------------------------------- orphans

func (a *App) diskOrphans(args []string) int {
	fs := flag.NewFlagSet("disk orphans", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	var scan stringList
	fs.Var(&scan, "scan", "another directory to search; may be given more than once")
	del := fs.Bool("delete", false, "delete what was found, after confirming")
	relinkTo := fs.String("relink", "", "point this distribution at a disk")
	to := fs.String("to", "", "the disk to point it at")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Stderr, "disk orphans takes no arguments, got %q\n", fs.Arg(0))
		return ExitUsage
	}

	// The two repoint flags need each other. A lone --to used to fall
	// through to a plain scan and exit 0, which reads as a repoint that
	// happened and did not.
	if (*relinkTo == "") != (*to == "") {
		fmt.Fprintln(a.Stderr, "--relink and --to go together: name the distribution and the disk to point it at")
		return ExitUsage
	}
	if *relinkTo != "" {
		return a.relinkTo(f, *relinkTo, *to)
	}

	e := diskEnv()
	list, warnings, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	// Configured roots are added to the built-in three, then the flag on top.
	cfg := a.diskConfigOrDefaults(e)
	roots := append(append([]string{}, cfg.ScanDirs...), scan...)

	orphans, scanWarnings, err := disk.ScanOrphans(e, list, roots)
	if err != nil {
		return a.diskFail(f, err)
	}
	for _, w := range scanWarnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	if !*del {
		if f.jsonOut {
			for _, o := range orphans {
				if err := disk.WriteJSONLine(a.Stdout, disk.OrphanJSON(o)); err != nil {
					fmt.Fprintf(a.Stderr, "error: %v\n", err)
					return ExitFindings
				}
			}
			return ExitOK
		}
		disk.RenderOrphans(a.Stdout, orphans)
		return ExitOK
	}
	return a.deleteOrphans(f, e, orphans)
}

func (a *App) deleteOrphans(f diskFlags, e disk.Env, orphans []disk.Orphan) int {
	if len(orphans) == 0 {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing to delete")
		}
		return ExitOK
	}

	if !f.jsonOut {
		disk.RenderOrphans(a.Stdout, orphans)
		fmt.Fprintf(a.Stdout, "\n%s would be freed\n\n", disk.FormatSize(disk.TotalSize(orphans)))
		fmt.Fprintf(a.Stdout, "%s\n", disk.UnclaimedIsNotUnused)
	}

	// Checked before the prompt: a dry run never asks.
	if f.dryRun {
		for _, o := range orphans {
			if f.jsonOut {
				if err := disk.WriteJSONLine(a.Stdout, map[string]any{"path": o.Path, "dry_run": true, "deleted": false}); err != nil {
					fmt.Fprintf(a.Stderr, "error: %v\n", err)
					return ExitFindings
				}
			}
		}
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "--dry-run: nothing was deleted")
		}
		return ExitOK
	}

	if !f.yes && !a.confirm(fmt.Sprintf("delete %d file(s)?", len(orphans))) {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing was deleted")
		}
		return ExitOK
	}

	results := disk.DeleteOrphans(e, orphans, e.FS.Remove)
	deleted, failed := 0, 0
	for _, r := range results {
		if r.Deleted {
			deleted++
		} else {
			failed++
		}
		if f.jsonOut {
			if err := disk.WriteJSONLine(a.Stdout, disk.DeleteJSON(r)); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			continue
		}
		if r.Deleted {
			fmt.Fprintf(a.Stdout, "deleted %s\n", r.Path)
		} else {
			fmt.Fprintf(a.Stderr, "error: %v\n", r.Err)
		}
	}
	switch {
	case failed == 0:
		return ExitOK
	case deleted == 0:
		return ExitDiskBusy
	default:
		return ExitDiskPartial
	}
}

// confirm asks one question for the whole set.
//
// One question, not one per file: a prompt repeated five times trains the
// answer rather than the decision. End of input is a no, because a piped
// command with nothing to answer with has not consented to anything.
func (a *App) confirm(question string) bool {
	if a.Stdin == nil {
		return false
	}
	fmt.Fprintf(a.Stdout, "%s [y/N] ", question)
	r := bufio.NewReader(a.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(a.Stdout)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------- relink

func (a *App) diskRelink(args []string) int {
	fs := flag.NewFlagSet("disk relink", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)

	// Two positionals, either before or after the flags.
	var positional []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") && len(positional) < 2 {
		positional = append(positional, args[0])
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	positional = append(positional, fs.Args()...)
	if len(positional) != 2 {
		fmt.Fprintln(a.Stderr, "usage: wslkit disk relink <distro> <path-to-vhdx>")
		return ExitUsage
	}
	return a.relinkWith(f, positional[0], positional[1])
}

// relinkTo is the orphans --relink entry point, which delegates so the two
// spellings cannot drift.
func (a *App) relinkTo(f diskFlags, name, target string) int {
	return a.relinkWith(f, name, target)
}

func (a *App) relinkWith(f diskFlags, name, target string) int {
	ctx := context.Background()
	e := diskEnv()
	list, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	r, err := disk.Resolve(list, name)
	if err != nil {
		return a.diskFail(f, err)
	}

	running, known := false, true
	if names, err := e.Host.Running(ctx); err != nil {
		known = false
	} else {
		for _, n := range names {
			if strings.EqualFold(n, r.Name) {
				running = true
			}
		}
	}

	plan, err := disk.PlanRelink(e, r, target, running, known)
	if err != nil {
		return a.diskFail(f, err)
	}
	if err := plan.Valid(); err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	res, err := disk.Relink(ctx, e, r, target, progress)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.RelinkJSON(res)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s now points at %s\n", res.Distro, res.VhdPath)
	return ExitOK
}
