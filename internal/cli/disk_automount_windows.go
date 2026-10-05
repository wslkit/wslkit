//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
	"github.com/wslkit/wslkit/internal/env/collect"
	"github.com/wslkit/wslkit/internal/schtask"
)

// AutomountTaskName is the logon task `disk automount install` registers.
const AutomountTaskName = `wslkit disk automount`

// diskAutomount is `disk automount add|list|rm|now|install|uninstall` (#88).
func (a *App) diskAutomount(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit disk automount add|list|rm|now|install|uninstall")
		return ExitUsage
	}
	switch args[0] {
	case "add":
		return a.automountAdd(args[1:])
	case "list":
		return a.automountList(args[1:])
	case "rm", "remove":
		return a.automountRemove(args[1:])
	case "now":
		return a.automountNow(args[1:])
	case "install":
		return a.automountInstall(args[1:])
	case "uninstall":
		return a.automountUninstall(args[1:])
	default:
		fmt.Fprintf(a.Stderr, "unknown automount subcommand %q: add, list, rm, now, install or uninstall\n", args[0])
		return ExitUsage
	}
}

// onePathArg parses a subcommand that takes exactly one path.
func onePathArg(a *App, fs *flag.FlagSet, args []string, command string) (string, int) {
	path := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		path, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", ExitUsage
	}
	extra := fs.Args()
	if path == "" && len(extra) > 0 {
		path, extra = extra[0], extra[1:]
	}
	if path == "" || len(extra) > 0 {
		fmt.Fprintf(a.Stderr, "usage: wslkit %s <path-to-vhdx>\n", command)
		return "", ExitUsage
	}
	return path, ExitOK
}

func (a *App) automountAdd(args []string) int {
	fs := flag.NewFlagSet("disk automount add", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	var entry disk.AutomountEntry
	fs.StringVar(&entry.Name, "name", "", "mount under /mnt/wsl/NAME")
	fs.BoolVar(&entry.Bare, "bare", false, "attach without mounting")
	fs.StringVar(&entry.Type, "type", "", "filesystem type, ext4 by default")
	fs.StringVar(&entry.Options, "options", "", "mount options")
	fs.IntVar(&entry.Partition, "partition", 0, "which partition to mount")
	path, code := onePathArg(a, fs, args, "disk automount add")
	if code != ExitOK {
		return code
	}
	entry.Path = path

	e := diskEnv()
	c, _, err := disk.LoadConfig(e.FS)
	if err != nil {
		return a.diskFail(f, err)
	}
	list, _, err := e.Registry.Distros()
	if err != nil {
		return a.diskFail(f, err)
	}
	if err := disk.ValidateAutomount(entry, c.Automount, list); err != nil {
		return a.diskFail(f, err)
	}
	if !e.FS.Exists(entry.Path) {
		// Allowed, since the disk may live on a drive that is not always
		// there, but said.
		fmt.Fprintf(a.Stderr, "note: %s is not there now; it is skipped until it is\n", entry.Path)
	}
	if f.dryRun {
		p := disk.Plan{Subject: entry.Path, SubjectKey: "path"}
		p.AddUndoable("add %s to the automount table: wsl %s", entry.Path, strings.Join(entry.MountArgs(), " "))
		return a.renderPlan(f, p)
	}
	c.Automount = append(c.Automount, entry)
	file, err := disk.SaveConfig(e.FS, c)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		o := disk.AutomountJSON(entry)
		o["added"] = true
		o["config"] = file
		return a.writeJSON(o)
	}
	fmt.Fprintf(a.Stdout, "added %s to %s\n", entry.Path, file)
	fmt.Fprintln(a.Stdout, "attach it now with an elevated: wslkit disk automount now")
	fmt.Fprintln(a.Stdout, "and at every logon with an elevated: wslkit disk automount install")
	return ExitOK
}

func (a *App) automountList(args []string) int {
	fs := flag.NewFlagSet("disk automount list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	e := diskEnv()
	c, _, err := disk.LoadConfig(e.FS)
	if err != nil {
		return a.diskFail(f, err)
	}
	installed := schtask.Exists(context.Background(), AutomountTaskName)
	if f.jsonOut {
		for _, entry := range c.Automount {
			o := disk.AutomountJSON(entry)
			o["present"] = e.FS.Exists(entry.Path)
			if code := a.writeJSON(o); code != ExitOK {
				return code
			}
		}
		return ExitOK
	}
	if len(c.Automount) == 0 {
		fmt.Fprintln(a.Stdout, "no disks in the automount table; add one with wslkit disk automount add <path>")
	} else {
		t := disk.Table{Headers: []string{"PATH", "MOUNT", "STATE"}}
		for _, entry := range c.Automount {
			state := "not there"
			if e.FS.Exists(entry.Path) {
				state = "not attached"
				if locked, err := e.FS.Locked(entry.Path); err == nil && locked {
					state = "attached or in use"
				}
			}
			t.Rows = append(t.Rows, []string{entry.Path, strings.Join(entry.MountArgs()[3:], " "), state})
		}
		fmt.Fprint(a.Stdout, t.String())
	}
	if installed {
		fmt.Fprintf(a.Stdout, "the logon task %q is installed\n", AutomountTaskName)
	} else {
		fmt.Fprintln(a.Stdout, "no logon task; an elevated wslkit disk automount install adds one")
	}
	return ExitOK
}

func (a *App) automountRemove(args []string) int {
	fs := flag.NewFlagSet("disk automount rm", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	path, code := onePathArg(a, fs, args, "disk automount rm")
	if code != ExitOK {
		return code
	}
	e := diskEnv()
	c, _, err := disk.LoadConfig(e.FS)
	if err != nil {
		return a.diskFail(f, err)
	}
	next, found := disk.RemoveAutomount(c.Automount, path)
	if !found {
		return a.diskFail(f, fmt.Errorf("%w: %s is not in the automount table", disk.ErrRefused, path))
	}
	if f.dryRun {
		p := disk.Plan{Subject: path, SubjectKey: "path"}
		p.Add("remove %s from the automount table; it stays attached until WSL next shuts down", path)
		return a.renderPlan(f, p)
	}
	c.Automount = next
	if _, err := disk.SaveConfig(e.FS, c); err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		return a.writeJSON(map[string]any{"path": path, "removed": true})
	}
	fmt.Fprintf(a.Stdout, "removed %s. If it is attached now it stays attached until WSL next shuts down; wsl --unmount detaches it sooner\n", path)
	return ExitOK
}

func (a *App) automountNow(args []string) int {
	fs := flag.NewFlagSet("disk automount now", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	e := diskEnv()
	c, _, err := disk.LoadConfig(e.FS)
	if err != nil {
		return a.diskFail(f, err)
	}
	if len(c.Automount) == 0 {
		if !f.jsonOut {
			fmt.Fprintln(a.Stdout, "no disks in the automount table")
		}
		return ExitOK
	}
	if f.dryRun {
		p := disk.Plan{Subject: "automount", SubjectKey: "table"}
		for _, entry := range c.Automount {
			p.Add("wsl %s", strings.Join(entry.MountArgs(), " "))
		}
		return a.renderPlan(f, p)
	}
	// wsl --mount needs an elevated process. Asking first gives the remedy,
	// where wsl.exe would print its own refusal once per disk.
	if !collect.IsElevated() {
		return a.diskFail(f, fmt.Errorf("%w: wsl --mount needs an elevated terminal. Run this from one, or install the logon task from one with wslkit disk automount install", disk.ErrRefused))
	}
	host := disk.NewHost()
	results := disk.ApplyAutomount(context.Background(), e.FS, host, c.Automount)
	failed := 0
	for _, r := range results {
		if r.Outcome == disk.AutomountFailed {
			failed++
		}
		if f.jsonOut {
			o := disk.AutomountJSON(r.Entry)
			o["outcome"] = string(r.Outcome)
			if r.Detail != "" {
				o["detail"] = r.Detail
			}
			if code := a.writeJSON(o); code != ExitOK {
				return code
			}
			continue
		}
		line := fmt.Sprintf("%s: %s", r.Entry.Path, r.Outcome)
		if r.Detail != "" {
			line += " (" + r.Detail + ")"
		}
		fmt.Fprintln(a.Stdout, line)
	}
	if failed > 0 {
		return ExitDiskPartial
	}
	return ExitOK
}

func (a *App) automountInstall(args []string) int {
	fs := flag.NewFlagSet("disk automount install", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	self, err := os.Executable()
	if err != nil {
		return a.diskFail(f, err)
	}
	u, err := user.Current()
	if err != nil {
		return a.diskFail(f, err)
	}
	logFile := ""
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		logFile = local + `\` + disk.Product + `\automount.log`
	}
	def := automountTaskXML(self, u.Username, logFile)
	if f.dryRun {
		p := disk.Plan{Subject: AutomountTaskName, SubjectKey: "task"}
		p.Add("register the logon task %q, running %s disk automount now with highest privileges", AutomountTaskName, self)
		return a.renderPlan(f, p)
	}
	// A task with highest privileges can only be registered from an
	// elevated process; schtasks would say "Access is denied", which reads
	// as something else.
	if !collect.IsElevated() {
		return a.diskFail(f, fmt.Errorf("%w: the task runs wsl --mount, which needs highest privileges, and registering one needs an elevated terminal once. Run this from one", disk.ErrRefused))
	}
	if err := schtask.Register(context.Background(), AutomountTaskName, def); err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		return a.writeJSON(map[string]any{"task": AutomountTaskName, "installed": true, "log": logFile})
	}
	fmt.Fprintf(a.Stdout, "installed the logon task %q. It attaches the table every time you log on", AutomountTaskName)
	if logFile != "" {
		fmt.Fprintf(a.Stdout, ", and writes what it did to %s", logFile)
	}
	fmt.Fprintln(a.Stdout, ".")
	return ExitOK
}

func (a *App) automountUninstall(args []string) int {
	fs := flag.NewFlagSet("disk automount uninstall", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	removed, err := schtask.Delete(context.Background(), AutomountTaskName)
	if err != nil {
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		return a.writeJSON(map[string]any{"task": AutomountTaskName, "removed": removed})
	}
	if removed {
		fmt.Fprintf(a.Stdout, "removed the logon task %q\n", AutomountTaskName)
	} else {
		fmt.Fprintln(a.Stdout, "no logon task was installed")
	}
	return ExitOK
}

// writeJSON prints one JSON line, reporting a failed write.
func (a *App) writeJSON(o map[string]any) int {
	if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitFindings
	}
	return ExitOK
}

// automountTaskXML is the logon task: run `disk automount now` with highest
// privileges, for this user, a little after logon so WSL's service is up.
func automountTaskXML(exe, username, logFile string) string {
	args := "disk automount now"
	if logFile != "" {
		args += ` --log "` + logFile + `"`
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Attaches the extra disks in wslkit's automount table to WSL. Installed by wslkit disk automount install.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Delay>PT30S</Delay>
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>`, schtask.XMLEscape(username), schtask.XMLEscape(username), schtask.XMLEscape(exe), schtask.XMLEscape(args))
}
