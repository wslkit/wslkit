//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/disk"
	"github.com/wslkit/wslkit/internal/fix"
)

// snapshotRoot is where snapshots are kept: beside the trash and the undo
// journal, under the kit's own directory.
func (a *App) snapshotRoot(e disk.Env) (string, error) {
	local, err := e.FS.ExpandEnv(`%LOCALAPPDATA%`)
	if err != nil || local == "" || local == `%LOCALAPPDATA%` {
		return "", fmt.Errorf("%w: %%LOCALAPPDATA%% is not set, so there is nowhere to keep snapshots", disk.ErrRefused)
	}
	return local + `\` + disk.Product + `\snapshots`, nil
}

// diskSnapshot is `disk snapshot <distro>`, `disk snapshot list [distro]` and
// `disk snapshot rm <distro> <id>` (#85).
func (a *App) diskSnapshot(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return a.snapshotList(args[1:])
		case "rm", "remove":
			return a.snapshotRemove(args[1:])
		}
	}
	fs := flag.NewFlagSet("disk snapshot", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	label := fs.String("name", "", "a label to recognise it by")
	shutdown := fs.Bool("shutdown", false, "permit stopping every distribution to free the disk")
	name, code := oneDistroArg(a, fs, args, "disk snapshot")
	if code != ExitOK {
		return code
	}

	ctx := context.Background()
	e := diskEnv()
	root, err := a.snapshotRoot(e)
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
	running, known := runningState(ctx, e, r.Name)
	o := disk.SnapshotOptions{Label: *label, Runtime: wslVersionLine(), Shutdown: *shutdown}
	plan, err := disk.PlanSnapshot(e, r, root, o, running, known)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}
	if running && !f.yes && !a.confirm(fmt.Sprintf("%s is running and will be stopped for the copy. Go ahead?", r.Name)) {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing was changed")
		}
		return ExitOK
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	entry, err := disk.TakeSnapshot(ctx, e, r, root, o, running, progress)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.SnapshotJSON(entry)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "snapshot %s of %s: %s, in %s\n", entry.Manifest.ID, r.Name, disk.FormatSize(entry.Manifest.SizeOnDisk), entry.Dir)
	fmt.Fprintf(a.Stdout, "restore it with: wslkit disk restore %s %s\n", r.Name, entry.Manifest.ID)
	return ExitOK
}

func (a *App) snapshotList(args []string) int {
	fs := flag.NewFlagSet("disk snapshot list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}

	e := diskEnv()
	root, err := a.snapshotRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
	all := disk.ListSnapshots(e, root)
	if name != "" {
		list, _, err := e.Registry.Distros()
		if err != nil {
			return a.diskFail(f, err)
		}
		r, err := disk.Resolve(list, name)
		if err != nil {
			return a.diskFail(f, err)
		}
		all = disk.SnapshotsOf(all, r)
	}
	if f.jsonOut {
		for _, s := range all {
			if err := disk.WriteJSONLine(a.Stdout, disk.SnapshotJSON(s)); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
		}
		return ExitOK
	}
	if len(all) == 0 {
		fmt.Fprintln(a.Stdout, "no snapshots")
		return ExitOK
	}
	t := disk.Table{Headers: []string{"DISTRIBUTION", "ID", "TAKEN", "SIZE ON DISK", "LABEL"}}
	for _, s := range all {
		label := s.Manifest.Label
		if s.Err != nil {
			label = "unreadable: " + s.Err.Error()
		}
		t.Rows = append(t.Rows, []string{
			s.Manifest.Distribution, s.Manifest.ID,
			s.Manifest.TakenAt.Local().Format("2006-01-02 15:04"),
			disk.FormatSize(s.Manifest.SizeOnDisk), label,
		})
	}
	fmt.Fprint(a.Stdout, t.String())
	return ExitOK
}

func (a *App) snapshotRemove(args []string) int {
	fs := flag.NewFlagSet("disk snapshot rm", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
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
		fmt.Fprintln(a.Stderr, "usage: wslkit disk snapshot rm <distro> <id>")
		return ExitUsage
	}
	e := diskEnv()
	root, err := a.snapshotRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
	list, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	r, err := disk.Resolve(list, positional[0])
	if err != nil {
		return a.diskFail(f, err)
	}
	s, err := disk.FindSnapshot(disk.ListSnapshots(e, root), r, positional[1])
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		p := disk.Plan{Subject: r.Name, SubjectKey: "distribution"}
		p.AddIrreversible("delete snapshot %s (%s)", s.Manifest.ID, s.Dir)
		return a.renderPlan(f, p)
	}
	if !f.yes && !a.confirm(fmt.Sprintf("delete snapshot %s of %s? This cannot be undone.", s.Manifest.ID, r.Name)) {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing was changed")
		}
		return ExitOK
	}
	if err := disk.RemoveSnapshot(e, s); err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{"distribution": r.Name, "id": s.Manifest.ID, "deleted": true}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "deleted snapshot %s of %s\n", s.Manifest.ID, r.Name)
	return ExitOK
}

// diskRestore is `disk restore <distro> <id|label|latest>`.
func (a *App) diskRestore(args []string) int {
	fs := flag.NewFlagSet("disk restore", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	shutdown := fs.Bool("shutdown", false, "permit stopping every distribution to free the disk")
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
		fmt.Fprintln(a.Stderr, "usage: wslkit disk restore <distro> <id|label|latest>")
		return ExitUsage
	}

	ctx := context.Background()
	e := diskEnv()
	root, err := a.snapshotRoot(e)
	if err != nil {
		return a.diskFail(f, err)
	}
	list, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	r, err := disk.Resolve(list, positional[0])
	if err != nil {
		return a.diskFail(f, err)
	}
	s, err := disk.FindSnapshot(disk.ListSnapshots(e, root), r, positional[1])
	if err != nil {
		return a.diskFail(f, err)
	}
	running, known := runningState(ctx, e, r.Name)
	plan, err := disk.PlanRestore(e, r, s, running, known)
	if err != nil {
		return a.diskFail(f, err)
	}
	if err := plan.Valid(); err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}
	if !f.yes && !a.confirm(fmt.Sprintf("replace the disk of %s with snapshot %s, taken %s? The current disk is kept as a snapshot.",
		r.Name, s.Manifest.ID, s.Manifest.TakenAt.Local().Format("2006-01-02 15:04"))) {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "nothing was changed")
		}
		return ExitOK
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	res, err := disk.Restore(ctx, e, r, s, root, running, *shutdown, progress)
	if err != nil {
		return a.diskFail(f, err)
	}

	// The disk it replaced is a snapshot now, so undoing the restore is
	// restoring that one: journalled, for `wslkit doctor undo`.
	undoID := ""
	if res.Before != "" {
		if self, err := os.Executable(); err == nil {
			p := fix.Plan{FixID: "disk-restore", Title: fmt.Sprintf("disk restore %s %s", r.Name, s.Manifest.ID), CreatedAt: time.Now(),
				Steps: []fix.Step{{Kind: "note", Description: fmt.Sprintf("restored %s from snapshot %s; the disk it replaced is snapshot %s", r.Name, s.Manifest.ID, res.Before)}},
				Rollback: []fix.Step{{Kind: "exec", Args: []string{self, "disk", "restore", r.Name, res.Before, "--yes"},
					Description: fmt.Sprintf("restore %s from snapshot %s, the disk it had before", r.Name, res.Before)}},
			}
			if id, err := (fix.Journal{Dir: fix.DefaultJournalDir()}).Save(p); err == nil {
				undoID = id
			}
		}
	}

	if f.jsonOut {
		o := map[string]any{"distribution": r.Name, "snapshot": s.Manifest.ID, "restored": true}
		if res.Before != "" {
			o["before"] = res.Before
		}
		if undoID != "" {
			o["undo"] = undoID
		}
		if res.BeforeErr != nil {
			o["warning"] = res.BeforeErr.Error()
		}
		if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s is back to snapshot %s, and it started.\n", r.Name, s.Manifest.ID)
	switch {
	case res.Before != "":
		fmt.Fprintf(a.Stdout, "the disk it replaced is snapshot %s\n", res.Before)
	case res.BeforeErr != nil:
		fmt.Fprintf(a.Stdout, "warning: %v\n", res.BeforeErr)
	}
	if undoID != "" {
		fmt.Fprintf(a.Stdout, "undo with: wslkit doctor undo %s\n", undoID)
	}
	return ExitOK
}
