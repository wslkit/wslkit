//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
)

func (a *App) diskRename(args []string) int {
	fs := flag.NewFlagSet("disk rename", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	f.registerForce(fs)

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
		fmt.Fprintln(a.Stderr, "usage: wslkit disk rename <distro> <new-name>")
		return ExitUsage
	}
	name, newName := positional[0], positional[1]

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
	// The Store launcher looks its distribution up by name.
	if err := disk.GuardPackaged(r, "rename", f.force); err != nil {
		return a.diskFail(f, err)
	}
	running, known := runningState(ctx, e, r.Name)

	plan, err := disk.PlanRename(e, r, newName, list, running, known)
	if err != nil {
		return a.diskFail(f, err)
	}
	if err := plan.Valid(); err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	// Journalled before the write: a rename that half-happens is still one
	// that `doctor undo` can take back.
	id, err := journalRegistry("disk-rename", fmt.Sprintf("disk rename %s %s", r.Name, newName), r.GUID,
		[]regValue{{Name: "DistributionName", Old: r.Name, Present: true}})
	if err != nil {
		return a.diskFail(f, fmt.Errorf("%w: the undo journal could not be written (%v), and a rename without one cannot be taken back", disk.ErrRefused, err))
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	res, err := disk.Rename(ctx, e, r, newName, progress)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		o := disk.RenameJSON(res)
		o["undo"] = id
		if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s is now %s.\n", res.OldName, res.NewName)
	fmt.Fprintf(a.Stdout, "undo with: wslkit doctor undo %s\n", id)
	return ExitOK
}

// runningState says whether one distribution is up, and whether that could be
// found out at all.
func runningState(ctx context.Context, e disk.Env, name string) (running, known bool) {
	names, err := e.Host.Running(ctx)
	if err != nil {
		return false, false
	}
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true, true
		}
	}
	return false, true
}
