//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
)

func (a *App) diskMove(args []string) int {
	fs := flag.NewFlagSet("disk move", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	keep := fs.Bool("keep-source", false, "leave the original file where it is")

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
		fmt.Fprintln(a.Stderr, "usage: wslkit disk move <distro> <destination-directory>")
		fmt.Fprintln(a.Stderr, "the disk keeps its file name, so the destination is a directory")
		return ExitUsage
	}
	name, destDir := positional[0], positional[1]

	ctx := context.Background()
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

	target := disk.NewMoveTarget(r.VhdPath(), destDir)
	o := disk.MoveOptions{KeepSource: *keep}
	plan, err := disk.PlanMove(e, r, target, o, running, known)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if err := plan.Valid(); err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitFindings
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	res, err := disk.Move(ctx, e, r, target, o, progress)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.MoveJSON(res)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	size := ""
	if res.SizeOnDisk != nil {
		size = " (" + disk.FormatSize(*res.SizeOnDisk) + ")"
	}
	fmt.Fprintf(a.Stdout, "%s now lives at %s%s\n", res.Distro, res.VhdPath, size)
	return ExitOK
}
