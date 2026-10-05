//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/wslkit/wslkit/internal/disk"
)

// diskFlagsCmd is `disk flags`: the interop, PATH and drive-mounting switches
// on a distribution's registration (#92).
func (a *App) diskFlagsCmd(args []string) int {
	fs := flag.NewFlagSet("disk flags", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	interop := fs.String("interop", "", "on or off: launch Windows programs from inside it")
	appendPath := fs.String("append-path", "", "on or off: add the Windows PATH to its PATH")
	automount := fs.String("automount", "", "on or off: mount the Windows drives under /mnt")
	name, code := oneDistroArg(a, fs, args, "disk flags")
	if code != ExitOK {
		return code
	}

	var change disk.FlagChange
	for _, s := range []struct {
		flag, value string
		into        **bool
	}{{"--interop", *interop, &change.Interop}, {"--append-path", *appendPath, &change.AppendPath}, {"--automount", *automount, &change.Automount}} {
		if s.value == "" {
			continue
		}
		v, err := disk.ParseOnOff(s.value)
		if err != nil {
			fmt.Fprintf(a.Stderr, "%s: %v\n", s.flag, err)
			return ExitUsage
		}
		*s.into = &v
	}

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

	// No switch named: show them.
	if change.Empty() {
		if f.jsonOut {
			if err := disk.WriteJSONLine(a.Stdout, disk.FlagsJSON(r.Name, r.Flags)); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			return ExitOK
		}
		fmt.Fprintf(a.Stdout, "%s  (Flags = %d)\n", r.Name, r.Flags)
		for _, l := range disk.FlagLines(r.Flags) {
			fmt.Fprintf(a.Stdout, "  %s\n", l)
		}
		return ExitOK
	}

	running, _ := runningState(ctx, e, r.Name)
	plan, next, err := disk.PlanFlags(r, change, running)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}
	old, present, err := e.Registry.ReadDWORD(r.GUID, "Flags")
	if err != nil {
		return a.diskFail(f, err)
	}
	id, err := journalRegistry("disk-flags", "disk flags "+r.Name, r.GUID, []regValue{dwordValue("Flags", old, present)})
	if err != nil {
		return a.diskFail(f, fmt.Errorf("%w: the undo journal could not be written: %v", disk.ErrRefused, err))
	}
	if err := e.Registry.WriteDWORD(r.GUID, "Flags", uint32(next)); err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		o := disk.FlagsJSON(r.Name, next)
		o["undo"] = id
		if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s  (Flags %d -> %d)\n", r.Name, r.Flags, next)
	for _, l := range disk.FlagLines(next) {
		fmt.Fprintf(a.Stdout, "  %s\n", l)
	}
	if running {
		fmt.Fprintf(a.Stdout, "takes effect after: wsl --terminate %s\n", r.Name)
	}
	fmt.Fprintf(a.Stdout, "undo with: wslkit doctor undo %s\n", id)
	return ExitOK
}

// diskDefaultUser is `disk default-user`: the account a distribution opens as.
func (a *App) diskDefaultUser(args []string) int {
	fs := flag.NewFlagSet("disk default-user", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)

	var positional []string
	for len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' && len(positional) < 2 {
		positional = append(positional, args[0])
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	positional = append(positional, fs.Args()...)
	if len(positional) != 2 {
		fmt.Fprintln(a.Stderr, "usage: wslkit disk default-user <distro> <uid|name>")
		return ExitUsage
	}
	name, who := positional[0], positional[1]

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
	running, _ := runningState(ctx, e, r.Name)

	uid, isNumber := disk.ParseUID(who)
	account := ""
	checked := false
	switch {
	case running:
		// Up already, so asking costs nothing: a name becomes its uid, and
		// a uid is proved to be an account.
		n, nm, err := disk.LookupUser(ctx, e, r.Name, who)
		if err != nil {
			return a.diskFail(f, err)
		}
		uid, account, checked = n, nm, true
	case !isNumber:
		return a.diskFail(f, fmt.Errorf("%w: %s is stopped, and turning a user name into a uid means starting it. Pass the uid, or start it first", disk.ErrRefused, r.Name))
	}

	p := disk.Plan{Subject: r.Name, SubjectKey: "distribution"}
	p.AddUndoable("set DefaultUid on %s from %d to %d", r.Name, r.DefaultUID, uid)
	if !checked {
		p.Warn(fmt.Sprintf("%s is stopped, so uid %d was not checked against its accounts", r.Name, uid),
			"a uid with no account opens a shell as that bare number")
	}
	if f.dryRun {
		return a.renderPlan(f, p)
	}
	old, present, err := e.Registry.ReadDWORD(r.GUID, "DefaultUid")
	if err != nil {
		return a.diskFail(f, err)
	}
	id, err := journalRegistry("disk-default-user", "disk default-user "+r.Name, r.GUID, []regValue{dwordValue("DefaultUid", old, present)})
	if err != nil {
		return a.diskFail(f, fmt.Errorf("%w: the undo journal could not be written: %v", disk.ErrRefused, err))
	}
	if err := e.Registry.WriteDWORD(r.GUID, "DefaultUid", uid); err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{
			"distribution": r.Name, "default_uid": uid, "user": account, "checked": checked, "undo": id,
		}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	who = strconv.FormatUint(uint64(uid), 10)
	if account != "" {
		who = account + " (uid " + who + ")"
	}
	fmt.Fprintf(a.Stdout, "%s now opens as %s.\n", r.Name, who)
	if !checked {
		fmt.Fprintf(a.Stdout, "%s is stopped, so the uid was not checked against its accounts.\n", r.Name)
	}
	fmt.Fprintf(a.Stdout, "undo with: wslkit doctor undo %s\n", id)
	return ExitOK
}
