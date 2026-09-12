// Package cli implements the subcommands. It is thin: parse flags, call
// collect / probe / render / fix, map to exit codes.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/env/collect"
	"github.com/wslkit/wsldoctor/internal/fix"
	"github.com/wslkit/wsldoctor/internal/fix/actions"
	fixexec "github.com/wslkit/wsldoctor/internal/fix/exec"
	"github.com/wslkit/wsldoctor/internal/probe"
	"github.com/wslkit/wsldoctor/internal/probe/all"
	"github.com/wslkit/wsldoctor/internal/render"
)

const (
	ExitOK        = 0
	ExitFindings  = 1
	ExitUsage     = 2
	ExitCollector = 3
)

type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
}

func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.usage()
		return ExitUsage
	}
	switch args[0] {
	case "check":
		return a.check(args[1:])
	case "fix":
		return a.fix(args[1:])
	case "undo":
		return a.undo(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(a.Stdout, "wsldoctor %s\n", a.Version)
		return ExitOK
	case "help", "--help", "-h":
		a.usage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n", args[0])
		a.usage()
		return ExitUsage
	}
}

func (a *App) usage() {
	fmt.Fprint(a.Stderr, `wsldoctor: diagnose why WSL 2 is broken or slow, then fix it.

  wsldoctor check [flags]         read-only, no admin, ranked diagnosis
  wsldoctor fix <id> [--apply]    plan (default) or apply one remediation
  wsldoctor undo [<journal-id>]   list journal entries, or replay one rollback
  wsldoctor version

check flags:
  --json                  machine-readable output (schema wsldoctor/result/v1)
  --report                redacted markdown block for bug reports
  --verbose               show details for OK and SKIPPED findings too
  --only M1[,M2]          run only probes tagged with these milestones
  --from-snapshot FILE    run probes on a saved --json output instead of this machine
  --elevated              require an elevated terminal (re-checks admin-only data)
  --allow-vm-wake         permit probes that would start the WSL VM (none yet)
  --timeout DURATION      per-collector deadline (default 5s)
  --no-redact             do not scrub user paths from human/json output

fixes: `+fixIDs()+`
`)
}

func fixIDs() string {
	var ids []string
	for _, f := range actions.All() {
		ids = append(ids, f.ID())
	}
	return strings.Join(ids, ", ")
}

func (a *App) check(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var (
		jsonOut  = fs.Bool("json", false, "")
		report   = fs.Bool("report", false, "")
		verbose  = fs.Bool("verbose", false, "")
		only     = fs.String("only", "", "")
		snapshot = fs.String("from-snapshot", "", "")
		elevated = fs.Bool("elevated", false, "")
		vmWake   = fs.Bool("allow-vm-wake", false, "")
		timeout  = fs.Duration("timeout", 5*time.Second, "")
		noRedact = fs.Bool("no-redact", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	var e *env.Env
	if *snapshot != "" {
		b, err := os.ReadFile(*snapshot)
		if err != nil {
			fmt.Fprintf(a.Stderr, "cannot read snapshot: %v\n", err)
			return ExitUsage
		}
		e, err = render.LoadSnapshot(b)
		if err != nil {
			fmt.Fprintf(a.Stderr, "bad snapshot: %v\n", err)
			return ExitUsage
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout*4)
		defer cancel()
		var err error
		e, err = collect.Run(ctx, collect.Options{Tool: "wsldoctor " + a.Version, AllowVMWake: *vmWake, Timeout: *timeout})
		if err != nil {
			fmt.Fprintf(a.Stderr, "%v\n", err)
			return ExitCollector
		}
		if *elevated && !e.Elevated {
			fmt.Fprintln(a.Stderr, "--elevated was given but this terminal is not elevated. Open an Administrator terminal and re-run.")
			return ExitUsage
		}
	}

	probes := all.Probes()
	if *only != "" {
		set := map[string]bool{}
		for _, m := range strings.Split(*only, ",") {
			set[strings.ToUpper(strings.TrimSpace(m))] = true
		}
		probes = probe.Filter(probes, set)
	}
	results := probe.RunAll(probes, e)

	opts := render.Options{Version: a.Version, Redact: !*noRedact, Verbose: *verbose}
	switch {
	case *jsonOut:
		if err := render.JSON(a.Stdout, e, results, opts); err != nil {
			fmt.Fprintf(a.Stderr, "render: %v\n", err)
			return ExitCollector
		}
	case *report:
		render.Report(a.Stdout, e, results, opts)
	default:
		render.Human(a.Stdout, e, results, opts)
	}
	if severeCollectorFailure(e) {
		fmt.Fprintln(a.Stderr, "warning: core collectors failed; results may be unreliable (see collectors in --json)")
		return ExitCollector
	}
	return probe.ExitCode(results)
}

// severeCollectorFailure is true when neither the runtime nor the OS could be read.
func severeCollectorFailure(e *env.Env) bool {
	return !e.Host.OS.OK() && !e.Runtime.Version.OK() && !e.Runtime.InboxWslVersion.OK()
}

func (a *App) fix(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.Stderr, "usage: wsldoctor fix <id> [--apply] [fix args]\nfixes: %s\n", fixIDs())
		return ExitUsage
	}
	id := args[0]
	f, ok := actions.Lookup(id)
	if !ok {
		fmt.Fprintf(a.Stderr, "unknown fix %q; fixes: %s\n", id, fixIDs())
		return ExitUsage
	}
	apply := false
	var rest []string
	for _, arg := range args[1:] {
		if arg == "--apply" {
			apply = true
			continue
		}
		rest = append(rest, arg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e, err := collect.Run(ctx, collect.Options{Tool: "wsldoctor " + a.Version})
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	if f.Elevates() && !e.Elevated {
		fmt.Fprintf(a.Stderr, "fix %s changes machine-wide settings and needs an elevated (Administrator) terminal.\n", f.ID())
		return ExitUsage
	}
	plan, err := f.Plan(e, fix.Options{Args: rest})
	if err != nil {
		fmt.Fprintf(a.Stderr, "planning failed: %v\n", err)
		return ExitCollector
	}
	fmt.Fprint(a.Stdout, fix.Describe(plan))
	if !apply {
		fmt.Fprintln(a.Stdout, "\nDry run. Re-run with --apply to execute.")
		return ExitOK
	}
	j := fix.Journal{Dir: fix.DefaultJournalDir()}
	jid, err := j.Save(plan)
	if err != nil {
		fmt.Fprintf(a.Stderr, "cannot write undo journal (%v); refusing to apply without one\n", err)
		return ExitCollector
	}
	fmt.Fprintf(a.Stdout, "\nApplying (undo id: %s)\n", jid)
	if err := fix.Apply(plan, fixexec.Real{Out: a.Stdout}); err != nil {
		fmt.Fprintf(a.Stderr, "\n%v\nRollback steps are saved; run: wsldoctor undo %s\n", err, jid)
		return ExitFindings
	}
	fmt.Fprintf(a.Stdout, "\nDone. To roll back: wsldoctor undo %s\n", jid)
	return ExitOK
}

func (a *App) undo(args []string) int {
	j := fix.Journal{Dir: fix.DefaultJournalDir()}
	if len(args) == 0 {
		ids, err := j.List()
		if err != nil {
			fmt.Fprintf(a.Stderr, "%v\n", err)
			return ExitCollector
		}
		if len(ids) == 0 {
			fmt.Fprintln(a.Stdout, "no fixes have been applied on this machine")
			return ExitOK
		}
		for _, id := range ids {
			fmt.Fprintln(a.Stdout, id)
		}
		return ExitOK
	}
	plan, err := j.Load(args[0])
	if err != nil {
		fmt.Fprintf(a.Stderr, "cannot load journal entry: %v\n", err)
		return ExitUsage
	}
	if len(plan.Rollback) == 0 {
		fmt.Fprintln(a.Stdout, "this fix recorded no rollback steps")
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "Rolling back %s (%s)\n", plan.FixID, plan.Title)
	rb := fix.Plan{FixID: plan.FixID, Title: "rollback of " + plan.Title, Steps: plan.Rollback}
	if err := fix.Apply(rb, fixexec.Real{Out: a.Stdout}); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	return ExitOK
}
