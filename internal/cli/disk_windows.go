//go:build windows

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/disk"
)

func (a *App) diskUsage() {
	fmt.Fprint(a.Stderr, `wslkit disk: inspect and maintain the virtual disks behind WSL 2 distributions.

  wslkit disk list [--probe]              every distribution and what its disk costs
  wslkit disk info <distro> [--probe]     everything known about one distribution
  wslkit disk trim <distro>               ask the guest to release the blocks it no longer uses
  wslkit disk compact [distro]            trim, stop, then shrink the disk file
  wslkit disk usage <distro>              where the space inside a distribution went
  wslkit disk orphans                     virtual disks that no distribution claims
  wslkit disk relink <distro> <path>      point a distribution at a disk that has moved
  wslkit disk move <distro> <directory>   move a distribution disk, then check it still boots
  wslkit disk config [path|get|set|edit]   show or change the settings

Flags common to every disk subcommand:
  --json        machine-readable output, one object per line, sizes in bytes
  --verbose     explain what is happening, on stderr
  --dry-run     show what would happen and change nothing
  -y, --yes     do not prompt for confirmation

  --probe       start a stopped distribution to read the usage inside it.
                Off by default: starting a distribution to measure it changes
                the thing being measured.

orphans flags:
  --scan DIR          another directory to search, on top of the built-in three.
                      May be given more than once
  --delete            delete what was found, after one confirmation for the set
  --relink DISTRO     point a distribution at a disk, with --to
  --to PATH           the disk to point it at

move flags:
  --keep-source       leave the original file where it is

usage flags:
  --top N             show only the largest N entries
  --by-directory      also break the whole guest down by directory
  --depth N           how deep that breakdown goes (1 to 8, default 2)

compact flags:
  --all               every WSL 2 distribution
  --file PATH         a loose .vhdx, such as the one Docker Desktop keeps
  --no-trim           skip the fstrim step. Compaction then reclaims almost
                      nothing, because the disk still holds the stale data
  --restart           start the distribution again afterwards if it was running
  --shutdown          permit stopping every distribution to free the disk
  --unlock-timeout D  how long to wait for the utility VM to let go (default 1m30s)

Sizes: "size on disk" is what the volume actually spends, which is the number
that changes when you free space. "virtual size" is the maximum the disk may
grow to and is normally 1 TiB whatever the distribution holds.
`)
}

// Exit codes specific to disk, extending the codes in cli.go. They keep the
// meanings the standalone wsldisk published, so scripts written against it keep
// working: 2 is a usage error and 3 is a refusal before anything ran, both of
// which already match, while 10 and 11 name the two failures worth branching
// on.
const (
	// ExitDiskPreflight means a check declined before anything ran, so
	// nothing changed. It shares its number with the kit's collector
	// failure, which carries the same meaning: the command did not start.
	ExitDiskPreflight = ExitCollector
	ExitDiskPartial   = 5
	ExitDiskNotFound  = 10
	ExitDiskBusy      = 11
)

func (a *App) disk(args []string) int {
	if len(args) == 0 {
		a.diskUsage()
		return ExitUsage
	}
	switch args[0] {
	case "list":
		return a.diskList(args[1:])
	case "info":
		return a.diskInfo(args[1:])
	case "trim":
		return a.diskTrim(args[1:])
	case "compact":
		return a.diskCompact(args[1:])
	case "usage":
		return a.diskUsageCmd(args[1:])
	case "orphans":
		return a.diskOrphans(args[1:])
	case "relink":
		return a.diskRelink(args[1:])
	case "move":
		return a.diskMove(args[1:])
	case "config":
		return a.diskConfig(args[1:])
	case "help", "--help", "-h":
		a.diskUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown disk subcommand %q\n\n", args[0])
		a.diskUsage()
		return ExitUsage
	}
}

// diskFlags are accepted by every disk subcommand.
type diskFlags struct {
	jsonOut bool
	verbose bool
	dryRun  bool
	yes     bool
	probe   bool
	timeout time.Duration
}

func (f *diskFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.jsonOut, "json", false, "machine-readable output on stdout")
	fs.BoolVar(&f.verbose, "verbose", false, "explain what is happening, on stderr")
	fs.BoolVar(&f.verbose, "v", false, "explain what is happening, on stderr")
	fs.BoolVar(&f.dryRun, "dry-run", false, "show what would happen and change nothing")
	fs.BoolVar(&f.yes, "yes", false, "do not prompt for confirmation")
	fs.BoolVar(&f.yes, "y", false, "do not prompt for confirmation")
	fs.DurationVar(&f.timeout, "timeout", 30*time.Second, "bound on each command run inside a distribution")
}

// diskEnv builds the production port set.
func diskEnv() disk.Env {
	return disk.Env{
		Registry: disk.NewRegistry(),
		FS:       disk.WindowsFS{},
		Disks:    disk.WindowsDisks{},
		Host:     disk.NewHost(),
		Clock:    disk.RealClock{},
	}
}

// diskExitFor maps a failure to the exit code that names it.
func diskExitFor(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, disk.ErrNotFound), errors.Is(err, disk.ErrNoDistros),
		errors.Is(err, disk.ErrNoDefault), errors.Is(err, disk.ErrAmbiguous):
		return ExitDiskNotFound
	case errors.Is(err, disk.ErrRunning), errors.Is(err, disk.ErrBusy):
		return ExitDiskBusy
	case errors.Is(err, disk.ErrNotWSL2), errors.Is(err, disk.ErrNotVHDX), errors.Is(err, disk.ErrRefused):
		return ExitDiskPreflight
	default:
		return ExitFindings
	}
}

// collect gathers the rows both list and info render, so the two commands
// cannot drift in how they measure.
func (a *App) diskRows(ctx context.Context, e disk.Env, f diskFlags, only string) ([]disk.Row, error) {
	list, warnings, err := e.Registry.Distros()
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}
	if only != "" {
		one, err := disk.Resolve(list, only)
		if err != nil {
			return nil, err
		}
		list = []disk.Registration{one}
	}
	if len(list) == 0 {
		return nil, nil
	}

	// Ask once for the whole listing rather than once per row.
	running := map[string]bool{}
	names, rerr := e.Host.Running(ctx)
	known := rerr == nil
	if rerr != nil && f.verbose {
		fmt.Fprintf(a.Stderr, "verbose: the running distributions could not be listed: %v\n", rerr)
	}
	for _, n := range names {
		running[n] = true
	}

	opts := disk.MeasureOptions{Probe: f.probe, Running: running, Timeout: f.timeout}
	rows := make([]disk.Row, 0, len(list))
	for _, r := range disk.Sorted(list) {
		row := disk.Row{Reg: r}
		if known {
			up := running[r.Name]
			row.Running = &up
		}
		if f.verbose {
			fmt.Fprintf(a.Stderr, "verbose: measuring %s\n", r.Name)
		}
		row.Info = disk.Measure(ctx, e, r, opts)
		rows = append(rows, row)
	}
	return rows, nil
}

func (a *App) diskList(args []string) int {
	fs := flag.NewFlagSet("disk list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	fs.BoolVar(&f.probe, "probe", false, "start stopped distributions to read guest usage")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Stderr, "disk list takes no arguments, got %q\n", fs.Arg(0))
		return ExitUsage
	}

	if f.dryRun {
		// Answer the flag rather than ignoring it: a user who reaches for
		// --dry-run wants to be told whether anything was at stake.
		return a.diskNothingToChange(f, "list")
	}

	ctx := context.Background()
	rows, err := a.diskRows(ctx, diskEnv(), f, "")
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}

	if f.jsonOut {
		for _, r := range rows {
			if err := disk.WriteJSONLine(a.Stdout, disk.ListJSON(r)); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
		}
		return ExitOK
	}
	disk.RenderList(a.Stdout, rows)
	return ExitOK
}

func (a *App) diskInfo(args []string) int {
	fs := flag.NewFlagSet("disk info", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	fs.BoolVar(&f.probe, "probe", false, "start the distribution if stopped, to read guest usage")
	// The name may come before the flags (`info Ubuntu --json`), which the
	// flag package would otherwise stop at, or after them. Only the first
	// token can be the name: any later bare word is a flag's value.
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
		fmt.Fprintf(a.Stderr, "disk info takes one distribution name, got %q as well\n", extra[0])
		return ExitUsage
	}
	if name == "" {
		fmt.Fprintln(a.Stderr, "disk info needs the name of a distribution")
		return ExitUsage
	}

	if f.dryRun {
		return a.diskNothingToChange(f, "info")
	}

	ctx := context.Background()
	rows, err := a.diskRows(ctx, diskEnv(), f, name)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if len(rows) == 0 {
		fmt.Fprintf(a.Stderr, "error: %v\n", disk.ErrNotFound)
		return ExitDiskNotFound
	}

	row := rows[0]
	key := disk.RegistryKeyFor(row.Reg.GUID)
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.InfoJSON(row, key)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	disk.RenderInfo(a.Stdout, row, key)
	return ExitOK
}

// diskNothingToChange answers --dry-run for the read-only subcommands. They
// take no measurements in this mode: a dry run should be free of side effects,
// not merely free of mutations, and starting a distribution to measure it is a
// side effect.
func (a *App) diskNothingToChange(f diskFlags, command string) int {
	const note = "this command only reads; there is nothing it would have changed"
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{
			"command": command,
			"dry_run": true,
			"note":    note,
		}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "--dry-run: %s\n", note)
	return ExitOK
}
