//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wslkit/wslkit/internal/guard"
)

func (a *App) guardUsage() {
	fmt.Fprint(a.Stderr, `wslkit guard: get WSL answering again after the machine has slept.

  wslkit guard run-once [--dry-run]    probe now, and recover if it is needed
  wslkit guard install [--elevated]    run it on resume and at logon
  wslkit guard uninstall               remove the scheduled tasks
  wslkit guard status                  what is installed, and what the last run did

run-once flags:
  --dry-run        decide everything, change nothing
  --elevated       permit the two steps that need administrator rights
  --max-step STEP  stop climbing after this step: shutdown, force, kill, restart
  --quiet          write to the log only, not to the terminal

WSL handles no power events. Nothing in it knows the machine slept, so the
sockets torn down entering Modern Standby are never rebuilt and the first
command after the lid opens hangs. This probes, and if something is genuinely
wedged, climbs a ladder: wsl --shutdown, then --force, then killing
wslservice.exe and restarting it. It never touches vmcompute or HvHost, which
have been reported to bluescreen the machine when stopped.

A distribution that was not running before the machine slept is not recovered:
starting one from cold is slow, not broken, and shutting WSL down every morning
is how a tool like this gets uninstalled.
`)
}

func (a *App) guard(args []string) int {
	if len(args) == 0 {
		a.guardUsage()
		return ExitUsage
	}
	switch args[0] {
	case "run-once":
		return a.guardRunOnce(args[1:])
	case "install":
		return a.guardInstall(args[1:])
	case "uninstall":
		return a.guardUninstall(args[1:])
	case "status":
		return a.guardStatus(args[1:])
	case "help", "--help", "-h":
		a.guardUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown guard subcommand %q\n\n", args[0])
		a.guardUsage()
		return ExitUsage
	}
}

func (a *App) guardRunOnce(args []string) int {
	fs := flag.NewFlagSet("guard run-once", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	dryRun := fs.Bool("dry-run", false, "decide everything and change nothing")
	elevated := fs.Bool("elevated", false, "permit the steps that need administrator rights")
	maxStep := fs.String("max-step", "", "stop after this step: shutdown, force, kill, restart")
	quiet := fs.Bool("quiet", false, "write to the log only")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rung, ok := guard.ParseRung(*maxStep)
	if !ok {
		fmt.Fprintf(a.Stderr, "--max-step %q is not a step: shutdown, force, kill or restart\n", *maxStep)
		return ExitUsage
	}

	// The log is the point of an unattended tool. A run that recovers the
	// machine and leaves no account of what it did is one nobody can trust
	// the next time it does something surprising.
	logFile, logErr := openLog()
	if logErr != nil {
		fmt.Fprintf(a.Stderr, "warning: %v\n", logErr)
	}
	var sink io.Writer = logFile
	switch {
	case logFile == nil && *quiet:
		sink = io.Discard
	case logFile == nil:
		sink = a.Stdout
	case !*quiet:
		sink = io.MultiWriter(logFile, a.Stdout)
	}
	if logFile != nil {
		defer func() { _ = logFile.Close() }()
	}

	ctx := context.Background()
	r := guard.WSLRunner{}

	// What the last run saw, which is the only evidence there is for
	// telling a hang from a cold start.
	var wasRunning []string
	known := false
	if s, ok := guard.LoadState(guard.StatePath()); ok && !s.Stale(r.Now(), guard.MaxStateAge) {
		wasRunning, known = s.Running, true
	}

	o := guard.Options{Elevated: *elevated, MaxRung: rung, DryRun: *dryRun}
	rep := guard.Run(ctx, r, o, wasRunning, known, sink)

	// Recorded after the run, so the next one knows what this one left
	// behind rather than what it found.
	if !*dryRun {
		if running, err := r.Running(ctx); err == nil {
			if err := guard.SaveState(guard.StatePath(), running, r.Now()); err != nil {
				fmt.Fprintf(sink, "could not record what is running: %v\n", err)
			}
		}
	}

	if !*quiet {
		a.renderGuard(rep)
	}
	switch {
	case rep.Health.Healthy(), rep.Recovered:
		return ExitOK
	case len(rep.Decision.Ladder) == 0:
		// Nothing was attempted, on purpose. That is not a failure.
		return ExitOK
	default:
		return ExitFindings
	}
}

func (a *App) renderGuard(rep guard.Report) {
	fmt.Fprintf(a.Stdout, "\n%s\n", rep.Decision.Reason)
	if rep.ClockStepped {
		fmt.Fprintln(a.Stdout, "the guest clock was stepped to match the host")
	}
	for _, at := range rep.Attempted {
		status := "did not help"
		if at.Healthy {
			status = "worked"
		}
		if at.Err != nil {
			status = fmt.Sprintf("%s (%v)", status, at.Err)
		}
		fmt.Fprintf(a.Stdout, "  %-24s %s\n", at.Rung, status)
	}
	if len(rep.Attempted) > 0 && !rep.Recovered {
		fmt.Fprintln(a.Stdout, "\nWSL still does not answer. The remaining step is a restart of Windows,")
		fmt.Fprintln(a.Stdout, "which this will not do for you.")
	}
	fmt.Fprintf(a.Stdout, "\nlog: %s\n", guard.LogPath())
}

func (a *App) guardInstall(args []string) int {
	fs := flag.NewFlagSet("guard install", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	elevated := fs.Bool("elevated", false, "also register a task with administrator rights")
	maxStep := fs.String("max-step", "", "stop after this step: shutdown, force, kill, restart")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rung, ok := guard.ParseRung(*maxStep)
	if !ok {
		fmt.Fprintf(a.Stderr, "--max-step %q is not a step\n", *maxStep)
		return ExitUsage
	}

	names, err := guard.Install(context.Background(), guard.InstallOptions{Elevated: *elevated, MaxRung: rung})
	for _, n := range names {
		fmt.Fprintf(a.Stdout, "registered %q\n", n)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		if *elevated && strings.Contains(err.Error(), "denied") {
			fmt.Fprintln(a.Stderr, "the elevated task needs an Administrator terminal, once, at install time")
		}
		return ExitCollector
	}
	fmt.Fprintf(a.Stdout, "\nIt runs %s after a resume and at logon, and writes to\n  %s\n",
		guard.SettleDelay, guard.LogPath())
	if !*elevated {
		fmt.Fprintln(a.Stdout, "\nWithout --elevated it can try wsl --shutdown and --force, which is enough for")
		fmt.Fprintln(a.Stdout, "most hangs. Killing and restarting the service needs administrator rights.")
	}
	return ExitOK
}

func (a *App) guardUninstall(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit guard uninstall")
		return ExitUsage
	}
	removed, err := guard.Uninstall(context.Background())
	for _, n := range removed {
		fmt.Fprintf(a.Stdout, "removed %q\n", n)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	if len(removed) == 0 {
		fmt.Fprintln(a.Stdout, "nothing was installed")
	}
	return ExitOK
}

func (a *App) guardStatus(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit guard status")
		return ExitUsage
	}
	for name, present := range guard.Status(context.Background()) {
		state := "not installed"
		if present {
			state = "installed"
		}
		fmt.Fprintf(a.Stdout, "%-28s %s\n", name, state)
	}

	if s, ok := guard.LoadState(guard.StatePath()); ok {
		what := "nothing running"
		if len(s.Running) > 0 {
			what = strings.Join(s.Running, ", ")
		}
		fmt.Fprintf(a.Stdout, "\nlast seen %s: %s\n", s.At.Local().Format("2006-01-02 15:04"), what)
	} else {
		fmt.Fprintln(a.Stdout, "\nno record of what was running yet: run `wslkit guard run-once` once to make one")
	}

	fmt.Fprintf(a.Stdout, "log: %s\n", guard.LogPath())
	if tail := tailLog(guard.LogPath(), 5); tail != "" {
		fmt.Fprintf(a.Stdout, "\n%s", tail)
	}
	return ExitOK
}

// openLog appends to the guard log, creating its directory.
func openLog() (*os.File, error) {
	path := guard.LogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("guard log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("guard log: %w", err)
	}
	return f, nil
}

// tailLog returns the last few lines, for `status`.
func tailLog(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
