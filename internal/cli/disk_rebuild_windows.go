//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/disk"
)

// diskRebuild exports a distribution and imports it again, keeping the
// registration.
//
// This is the answer to the disk that compaction cannot shrink. Compaction
// works in whole blocks, so free space scattered through the filesystem in
// small holes leaves most blocks partly used and reclaims almost nothing.
// Writing every file afresh into a new disk does not have that problem.
func (a *App) diskRebuild(args []string) int {
	fs := flag.NewFlagSet("disk rebuild", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)
	workDir := fs.String("work-dir", "", "where to write the intermediate archive (default: beside the disk)")
	keepArchive := fs.Bool("keep-archive", false, "keep the archive afterwards, as a backup")
	restart := fs.Bool("restart", false, "start the distribution again afterwards if it was running")
	// Not --timeout: that one is already taken for the commands run inside a
	// distribution, and an export of tens of gigabytes is a different
	// quantity entirely.
	transferTimeout := fs.Duration("transfer-timeout", disk.DefaultRebuildTimeout, "how long to allow for the export and for the import")

	name, code := oneDistroArg(a, fs, args, "disk rebuild")
	if code != ExitOK {
		return code
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

	running, known := false, true
	names, err := e.Host.Running(ctx)
	if err != nil {
		known = false
	}
	runningBefore := map[string]bool{}
	for _, n := range names {
		runningBefore[n] = true
		if strings.EqualFold(n, r.Name) {
			running = true
		}
	}

	o := disk.RebuildOptions{
		WorkDir:       *workDir,
		KeepArchive:   *keepArchive,
		Restart:       *restart,
		ExportTimeout: *transferTimeout,
		ImportTimeout: *transferTimeout,
		RunningBefore: runningBefore,
	}
	plan, err := disk.PlanRebuild(e, r, o, running, known)
	if err != nil {
		return a.diskFail(f, err)
	}
	if err := plan.Valid(); err != nil {
		return a.diskFail(f, err)
	}
	if f.dryRun {
		return a.renderPlan(f, plan)
	}

	// One confirmation, because this deletes the old disk. The archive is
	// the safety net and the prompt says so, but a command that unregisters
	// a distribution should never be one keystroke away.
	if !f.yes {
		if f.jsonOut {
			fmt.Fprintln(a.Stderr, "--json with rebuild needs -y: this unregisters the distribution and deletes its disk, and nothing can answer a prompt in the middle of a JSON stream")
			return ExitUsage
		}
		if !a.confirm(fmt.Sprintf("rebuild %s? The old disk is deleted and the distribution gets a new GUID.", r.Name)) {
			fmt.Fprintln(a.Stdout, "nothing was rebuilt")
			return ExitOK
		}
	}

	var progress disk.Progress = disk.DiscardProgress{}
	if !f.jsonOut {
		progress = disk.ConsoleProgress{W: a.Stderr, Ctx: ctx}
	}
	started := time.Now()
	res, err := disk.Rebuild(ctx, e, r, o, progress)
	if err != nil {
		// Even a failed rebuild has a result worth printing: it says where
		// the archive is, which is how the distribution comes back.
		if !f.jsonOut && res.ArchiveKept {
			fmt.Fprintf(a.Stdout, "the archive is at %s\n", res.Archive)
		}
		return a.diskFail(f, err)
	}
	if f.jsonOut {
		m := disk.RebuildJSON(res)
		m["seconds"] = int(time.Since(started).Seconds())
		if err := disk.WriteJSONLine(a.Stdout, m); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	disk.RenderRebuild(a.Stdout, res)
	return ExitOK
}
