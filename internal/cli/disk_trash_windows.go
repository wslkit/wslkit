//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
)

// trashRoot is where trashed distributions are kept, under the same directory
// the kit already uses for its undo journal.
func (a *App) trashRoot(e disk.Env) (string, error) {
	local, err := e.FS.ExpandEnv(`%LOCALAPPDATA%`)
	if err != nil || local == "" || local == `%LOCALAPPDATA%` {
		return "", fmt.Errorf("%w: %%LOCALAPPDATA%% is not set, so there is nowhere to keep trashed distributions", disk.ErrRefused)
	}
	return local + `\` + disk.Product + `\trash`, nil
}

func (a *App) diskTrash(args []string) int {
	fs := flag.NewFlagSet("disk trash", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	list := fs.Bool("list", false, "show what is in the trash")
	purge := fs.Bool("purge", false, "delete trashed distributions permanently")
	olderThan := fs.String("older-than", "", "with --purge, only entries at least this old, e.g. 30d")
	shutdown := fs.Bool("shutdown", false, "permit stopping every distribution to free the disk")

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
		fmt.Fprintf(a.Stderr, "disk trash takes one distribution name, got %q as well\n", extra[0])
		return ExitUsage
	}

	switch {
	case *list && *purge:
		fmt.Fprintln(a.Stderr, "--list and --purge do different things; pick one")
		return ExitUsage
	case (*list || *purge) && name != "":
		fmt.Fprintf(a.Stderr, "--list and --purge act on the whole trash, so they take no distribution name; got %q\n", name)
		return ExitUsage
	case *olderThan != "" && !*purge:
		fmt.Fprintln(a.Stderr, "--older-than only means something with --purge")
		return ExitUsage
	case *list:
		return a.trashList(f)
	case *purge:
		return a.trashPurge(f, *olderThan)
	case name == "":
		fmt.Fprintln(a.Stderr, "disk trash needs the name of a distribution, or --list, or --purge")
		return ExitUsage
	}
	return a.trashOne(f, name, *shutdown)
}

func (a *App) trashOne(f diskFlags, name string, shutdown bool) int {
	ctx := context.Background()
	e := diskEnv()
	root, err := a.trashRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
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

	plan, err := disk.PlanTrash(e, r, running, known)
	if err != nil {
		return a.diskFail(f, err)
	}
	if err := plan.Valid(); err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	// Unregistering is not reversible by WSL, only by this command, so it is
	// worth one question unless the caller has said not to ask.
	if !f.yes && !a.confirm(fmt.Sprintf("unregister %s? Its disk is kept, and wslkit disk undelete %s brings it back.", r.Name, r.Name)) {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing was changed")
		}
		return ExitOK
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	entry, err := disk.Trash(ctx, e, r, root, disk.TrashOptions{Shutdown: shutdown}, progress)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		o := disk.TrashJSON(entry, e.Clock.Now())
		o["trashed"] = true
		if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s is unregistered. Its disk is in %s.\n", r.Name, entry.Dir)
	fmt.Fprintf(a.Stdout, "bring it back with: wslkit disk undelete %s\n", r.Name)
	return ExitOK
}

func (a *App) trashList(f diskFlags) int {
	e := diskEnv()
	root, err := a.trashRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
	entries, err := disk.ListTrash(e, root)
	if err != nil {
		return a.diskFail(f, err)
	}
	now := e.Clock.Now()
	if f.jsonOut {
		for _, entry := range entries {
			if err := disk.WriteJSONLine(a.Stdout, disk.TrashJSON(entry, now)); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
		}
		return ExitOK
	}
	disk.RenderTrashList(a.Stdout, entries, now)
	return ExitOK
}

func (a *App) trashPurge(f diskFlags, olderThan string) int {
	e := diskEnv()
	root, err := a.trashRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
	age, err := disk.ParseAge(olderThan)
	if err != nil {
		return a.diskFail(f, err)
	}
	entries, err := disk.ListTrash(e, root)
	if err != nil {
		return a.diskFail(f, err)
	}
	now := e.Clock.Now()
	entries = disk.OlderThan(entries, age, now)

	if len(entries) == 0 {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing to purge")
		}
		return ExitOK
	}

	if !f.jsonOut {
		disk.RenderTrashList(a.Stdout, entries, now)
		fmt.Fprintf(a.Stdout, "\n%s would be freed permanently\n", disk.FormatSize(disk.TotalTrashBytes(entries)))
	}

	// Checked before the prompt: a dry run never asks.
	if f.dryRun {
		if f.jsonOut {
			for _, entry := range entries {
				o := disk.TrashJSON(entry, now)
				o["dry_run"] = true
				o["deleted"] = false
				if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
					fmt.Fprintf(a.Stderr, "error: %v\n", err)
					return ExitFindings
				}
			}
			return ExitOK
		}
		fmt.Fprintln(a.Stdout, "--dry-run: nothing was deleted")
		return ExitOK
	}

	// This is the one place in the kit where a distribution is destroyed
	// beyond recovery, so it asks even though the earlier step did.
	if !f.yes && !a.confirm(fmt.Sprintf("permanently delete %d trashed distribution(s)? This cannot be undone.", len(entries))) {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing was deleted")
		}
		return ExitOK
	}

	results := disk.Purge(e, entries)
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
		return ExitFindings
	default:
		return ExitDiskPartial
	}
}

// ---------------------------------------------------------------- undelete

func (a *App) diskUndelete(args []string) int {
	fs := flag.NewFlagSet("disk undelete", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	name, code := oneDistroArg(a, fs, args, "disk undelete")
	if code != ExitOK {
		return code
	}

	ctx := context.Background()
	e := diskEnv()
	root, err := a.trashRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
	entries, err := disk.ListTrash(e, root)
	if err != nil {
		return a.diskFail(f, err)
	}
	entry, err := disk.FindTrashed(entries, name)
	if err != nil {
		return a.diskFail(f, err)
	}
	registered, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	plan, err := disk.PlanUndelete(entry, registered)
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
	fresh, err := disk.Undelete(ctx, e, entry, progress)
	if err != nil {
		// A partial restore still leaves a usable distribution, so the
		// name is reported alongside what went wrong.
		if fresh.Name != "" && !f.jsonOut {
			fmt.Fprintf(a.Stdout, "%s is registered again, from %s\n", fresh.Name, entry.Dir)
		}
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{
			"distribution": fresh.Name,
			"guid":         fresh.GUID,
			"vhdx_path":    fresh.VhdPath(),
			"restored":     true,
		}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s is registered again, from %s\n", fresh.Name, entry.Dir)
	fmt.Fprintf(a.Stdout, "its disk is still in the trash folder; move it somewhere permanent with: wslkit disk move %s <directory>\n", fresh.Name)
	return ExitOK
}
