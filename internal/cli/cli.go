// Package cli implements the wslkit command tree. It is thin: parse flags, call
// collect / probe / render / fix, map to exit codes.
//
// Each command group prints its own usage page, the doctor included:
//
//	wslkit doctor ...  diagnose why WSL is broken or slow, then fix it
//	wslkit disk ...    inspect and maintain distribution disks
//	wslkit top         what the utility VM is using, and which distribution
//	wslkit limit ...   cap what one distribution may use
//	wslkit proxy ...   get a Windows proxy working inside a distribution
//	wslkit guard ...   get WSL answering again after the machine has slept
//	wslkit agent ...   the guest agent and the Windows daemon it talks to
//	wslkit sock ...    bridge Windows sockets into a distribution
//	wslkit version
package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/env/collect"
	"github.com/wslkit/wslkit/internal/fix"
	"github.com/wslkit/wslkit/internal/fix/actions"
	fixexec "github.com/wslkit/wslkit/internal/fix/exec"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/probe/all"
	"github.com/wslkit/wslkit/internal/render"
	"github.com/wslkit/wslkit/internal/wslerr"
)

const (
	ExitOK        = 0
	ExitFindings  = 1
	ExitUsage     = 2
	ExitCollector = 3
)

// Product is the binary and package name.
const Product = "wslkit"

type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	// Stdin is read only to answer a confirmation prompt. A nil Stdin, or
	// one at end of input, is a "no": a piped command with nothing to answer
	// with has not consented to anything.
	Stdin io.Reader
}

// Run dispatches the top-level command tree.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.usage()
		return ExitUsage
	}
	switch args[0] {
	case "doctor":
		return a.doctor(args[1:])
	case "agent":
		return a.agent(args[1:])
	case "sock":
		return a.sock(args[1:])
	case "disk":
		return a.disk(args[1:])
	case "top":
		return a.top(args[1:])
	case "proxy":
		return a.proxy(args[1:])
	case "guard":
		return a.guard(args[1:])
	case "limit":
		return a.limit(args[1:])
	case "completion":
		return a.completion(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(a.Stdout, "%s %s\n", Product, a.Version)
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

// doctor dispatches the diagnosis subcommands. With no subcommand (or only
// flags) it runs check, like `brew doctor`. `--help` is the exception: every
// other group answers it with its own page, so this one does too, rather than
// handing the flag to check and printing the flag package's bare list.
func (a *App) doctor(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		a.doctorUsage()
		return ExitOK
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return a.check(args)
	}
	switch args[0] {
	case "check":
		return a.check(args[1:])
	case "explain":
		return a.explain(args[1:])
	case "preflight":
		return a.preflight(args[1:])
	case "fix":
		return a.fix(args[1:])
	case "undo":
		return a.undo(args[1:])
	case "version", "--version", "-v":
		// Reached through the wsldoctor alias, which prefixes every argument
		// with "doctor". Without this, `wsldoctor version` is an unknown
		// subcommand followed by a page of usage, which is a poor greeting
		// for somebody checking what they have just installed.
		fmt.Fprintf(a.Stdout, "%s %s\n", Product, a.Version)
		return ExitOK
	case "help":
		a.doctorUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown doctor subcommand %q\n\n", args[0])
		a.doctorUsage()
		return ExitUsage
	}
}

func (a *App) usage() {
	fmt.Fprint(a.Stderr, `wslkit: a toolkit for WSL 2. Troubleshooting, diagnosis and maintenance,
in one binary. Every group prints its own help: wslkit <command> help.

  wslkit doctor ...                    diagnose why WSL is broken or slow, then fix it (wslkit doctor help)
  wslkit agent ...                     guest agent: install into a distro, run the Windows daemon (wslkit agent help)
  wslkit sock ...                      bridge Windows sockets into a distro: ssh-agent, gpg-agent (wslkit sock help)
  wslkit disk ...                      inspect and maintain distribution disks: list, info (wslkit disk help)
  wslkit top [--json] [--watch]        what the utility VM is using, and which distro
  wslkit limit ...                     cap what one distro may use (wslkit limit help)
  wslkit proxy ...                     get a Windows proxy working inside a distro (wslkit proxy help)
  wslkit guard ...                     get WSL answering again after the machine slept (wslkit guard help)
  wslkit completion <shell>            a completion script for powershell, bash or zsh
  wslkit version
  wslkit help
`)
}

// doctorUsage is the doctor's own page. It used to be the top-level usage: the
// kit grew out of a single doctor command, and for a while the help still read
// that way. Every other group prints its own, and so does this one.
func (a *App) doctorUsage() {
	fmt.Fprint(a.Stderr, `wslkit doctor: diagnose why WSL is broken or slow, then fix it.

  wslkit doctor [check] [flags]        read-only, no admin, ranked diagnosis
  wslkit doctor explain [flags] <err>  decode a WSL error code and run the probes that explain it
  wslkit doctor preflight <file.wsl>   check a distribution file before installing it
  wslkit doctor fix <id> [--apply]     plan (default) or apply one remediation
  wslkit doctor undo [<journal-id>]    list journal entries, or replay one rollback
                                       (--dry-run to see it first; -y to skip the prompt)

check / explain flags:
  --json                  machine-readable output (schema wslkit/result/v1)
  --report                redacted markdown block for bug reports
  --verbose               show details for OK and SKIPPED findings too
  --only M1[,M2]          run only probes tagged with these milestones (check)
  --from-snapshot FILE    run probes on a saved --json output instead of this machine
  --elevated              fail unless the terminal is elevated; elevation itself
                          is detected, and admin-only facts are read whenever it is
  --allow-vm-wake         permit probes that would start the WSL VM (none yet)
  --online                check the published WSL releases (one request, cached
                          for a day); off by default, nothing else uses the network
  --wslc                  also test DNS inside wslc containers (WSC001); starts a
                          stopped wslc session VM, so off by default
  --timeout DURATION      per-collector deadline (default 5s)
  --no-redact             do not scrub user paths from human/json output

explain accepts anything wsl.exe printed: "Error code: Wsl/Service/E_UNEXPECTED",
a bare 0x80370102, an error name, or the exit code 4294967295.

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

// runFlags are shared by check and explain.
type runFlags struct {
	jsonOut, report, verbose, elevated, vmWake, noRedact, online, wslc bool
	only, snapshot                                                     string
	timeout                                                            time.Duration
}

func (a *App) bind(fs *flag.FlagSet) *runFlags {
	rf := &runFlags{}
	fs.BoolVar(&rf.jsonOut, "json", false, "")
	fs.BoolVar(&rf.report, "report", false, "")
	fs.BoolVar(&rf.verbose, "verbose", false, "")
	fs.StringVar(&rf.only, "only", "", "")
	fs.StringVar(&rf.snapshot, "from-snapshot", "", "")
	fs.BoolVar(&rf.elevated, "elevated", false, "")
	fs.BoolVar(&rf.vmWake, "allow-vm-wake", false, "")
	fs.DurationVar(&rf.timeout, "timeout", 5*time.Second, "")
	fs.BoolVar(&rf.noRedact, "no-redact", false, "")
	fs.BoolVar(&rf.online, "online", false, "")
	fs.BoolVar(&rf.wslc, "wslc", false, "")
	// --offline is the default and is accepted so a script can say so.
	fs.Bool("offline", false, "")
	return rf
}

func (a *App) toolName() string { return Product + " " + a.Version }

// environment loads a snapshot or collects live. Returns an exit code on failure.
func (a *App) environment(rf *runFlags) (*env.Env, int) {
	if rf.snapshot != "" {
		b, err := os.ReadFile(rf.snapshot)
		if err != nil {
			fmt.Fprintf(a.Stderr, "cannot read snapshot: %v\n", err)
			return nil, ExitUsage
		}
		e, err := render.LoadSnapshot(b)
		if err != nil {
			fmt.Fprintf(a.Stderr, "bad snapshot: %v\n", err)
			return nil, ExitUsage
		}
		return e, ExitOK
	}
	limit := rf.timeout * 4
	if rf.wslc && limit < 90*time.Second {
		// The wslc collector may boot a VM, and has a minute of its own.
		limit = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	e, err := collect.Run(ctx, collect.Options{Tool: a.toolName(), AllowVMWake: rf.vmWake, Online: rf.online, WSLC: rf.wslc, Timeout: rf.timeout})
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return nil, ExitCollector
	}
	if rf.elevated && !e.Elevated {
		fmt.Fprintln(a.Stderr, "--elevated was given but this terminal is not elevated. Open an Administrator terminal and re-run.")
		return nil, ExitUsage
	}
	return e, ExitOK
}

// runAndRender runs probes on e and writes the chosen format. Returns the exit code.
func (a *App) runAndRender(e *env.Env, probes []probe.Probe, rf *runFlags) int {
	results := probe.RunAll(probes, e)
	opts := render.Options{Version: a.Version, Redact: !rf.noRedact, Verbose: rf.verbose}
	switch {
	case rf.jsonOut:
		if err := render.JSON(a.Stdout, e, results, opts); err != nil {
			fmt.Fprintf(a.Stderr, "render: %v\n", err)
			return ExitCollector
		}
	case rf.report:
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

func (a *App) check(args []string) int {
	fs := flag.NewFlagSet("doctor check", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	rf := a.bind(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	e, code := a.environment(rf)
	if e == nil {
		return code
	}
	probes := all.Probes()
	if rf.only != "" {
		set := map[string]bool{}
		for _, m := range strings.Split(rf.only, ",") {
			set[strings.ToUpper(strings.TrimSpace(m))] = true
		}
		probes = probe.Filter(probes, set)
	}
	return a.runAndRender(e, probes, rf)
}

func (a *App) explain(args []string) int {
	fs := flag.NewFlagSet("doctor explain", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	rf := a.bind(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		fmt.Fprintln(a.Stderr, "usage: wslkit doctor explain [flags] <error text>   e.g. explain Wsl/Service/E_UNEXPECTED")
		return ExitUsage
	}
	parsed, err := wslerr.Parse(text)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\nPaste the line that starts with \"Error code:\", a 0x... HRESULT, or the exit code.\n", err)
		return ExitUsage
	}
	dict, err := data.LoadErrors()
	if err != nil {
		fmt.Fprintf(a.Stderr, "error dictionary: %v\n", err)
		return ExitCollector
	}
	ex := dict.Explain(parsed)
	if !rf.jsonOut {
		a.printExplanation(ex)
	}
	if len(ex.Probes) == 0 && !rf.jsonOut {
		fmt.Fprintln(a.Stdout, "No probes are mapped to this error yet; running the full check instead.")
	}
	e, code := a.environment(rf)
	if e == nil {
		return code
	}
	probes := all.Probes()
	if len(ex.Probes) > 0 {
		probes = all.ByID(ex.Probes)
		if !rf.jsonOut {
			fmt.Fprintf(a.Stdout, "Running %d related probe(s):\n\n", len(probes))
		}
	}
	return a.runAndRender(e, probes, rf)
}

func (a *App) printExplanation(ex data.Explanation) {
	w := a.Stdout
	fmt.Fprintf(w, "Error: %s\n", ex.Parsed.Raw)
	for _, st := range ex.Steps {
		desc := st.Desc
		if desc == "" {
			if st.Known {
				desc = "(no description yet)"
			} else {
				desc = "(not a known WSL context; newer runtime or a typo)"
			}
		}
		fmt.Fprintf(w, "  %-26s %s\n", st.Name, desc)
		if st.Note != "" {
			fmt.Fprintf(w, "  %-26s note: %s\n", "", st.Note)
		}
	}
	code := ex.CodeName
	if ex.Code != nil && ex.Code.HRESULT != "" && !strings.HasPrefix(code, "0x") {
		code += "  " + ex.Code.HRESULT
	}
	switch {
	case ex.Code != nil:
		fmt.Fprintf(w, "  %-26s %s\n", code, ex.Code.Desc)
		if ex.Code.Note != "" {
			fmt.Fprintf(w, "  %-26s note: %s\n", "", ex.Code.Note)
		}
		if len(ex.Code.Causes) > 0 {
			fmt.Fprintln(w, "Known causes:")
			for i, c := range ex.Code.Causes {
				fmt.Fprintf(w, "  %d. %s", i+1, c.Summary)
				if len(c.Probes) > 0 {
					fmt.Fprintf(w, "   -> %s", strings.Join(c.Probes, ", "))
				}
				fmt.Fprintln(w)
			}
		}
		for _, r := range ex.Code.Refs {
			fmt.Fprintf(w, "  ref: %s\n", r)
		}
	case ex.KnownToWSL:
		fmt.Fprintf(w, "  %-26s (WSL knows this code by name; wslkit has no note for it yet)\n", code)
	default:
		fmt.Fprintf(w, "  %-26s (unknown to wslkit)\n", code)
	}
	fmt.Fprintln(w)
}

// severeCollectorFailure is true when neither the runtime nor the OS could be read.
func severeCollectorFailure(e *env.Env) bool {
	return !e.Host.OS.OK() && !e.Runtime.Version.OK() && !e.Runtime.InboxWslVersion.OK()
}

func (a *App) fix(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.Stderr, "usage: wslkit doctor fix <id> [--apply] [fix args]\nfixes: %s\n", fixIDs())
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
	e, err := collect.Run(ctx, collect.Options{Tool: a.toolName()})
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
		fmt.Fprintf(a.Stderr, "\n%v\nRollback steps are saved; run: wslkit doctor undo %s\n", err, jid)
		return ExitFindings
	}
	fmt.Fprintf(a.Stdout, "\nDone. To roll back: wslkit doctor undo %s\n", jid)
	return ExitOK
}

// undo lists the journal, or replays one rollback.
//
// The flags are parsed rather than ignored, and that is not a detail. An
// earlier version took args[0] as the id and dropped everything after it, so
// `doctor undo <id> --dry-run` silently performed the rollback: the one command
// in the tool whose whole purpose is to change the machine back, taking an
// instruction not to change anything and doing it anyway. It is the only
// destructive command here that had no dry run and no confirmation.
func (a *App) undo(args []string) int {
	fs := flag.NewFlagSet("doctor undo", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	dryRun := fs.Bool("dry-run", false, "show the rollback steps and change nothing")
	yes := fs.Bool("y", false, "do not prompt for confirmation")
	fs.BoolVar(yes, "yes", false, "do not prompt for confirmation")
	// The id may come before the flags, which is how anybody would type it.
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if id == "" && fs.NArg() > 0 {
		id = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fmt.Fprintf(a.Stderr, "doctor undo takes one journal id, got %q as well\n", fs.Arg(0))
		return ExitUsage
	}
	args = nil
	if id != "" {
		args = []string{id}
	}

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
	rb := fix.Plan{FixID: plan.FixID, Title: "rollback of " + plan.Title, Steps: plan.Rollback}
	if *dryRun {
		fmt.Fprint(a.Stdout, fix.Describe(rb))
		fmt.Fprintln(a.Stdout, "\n--dry-run: nothing was rolled back.")
		return ExitOK
	}
	fmt.Fprint(a.Stdout, fix.Describe(rb))
	if !*yes && !a.confirmUndo(plan.FixID) {
		fmt.Fprintln(a.Stdout, "nothing was rolled back")
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "Rolling back %s (%s)\n", plan.FixID, plan.Title)
	if err := fix.Apply(rb, fixexec.Real{Out: a.Stdout}); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	return ExitOK
}

// confirmUndo asks before replaying a rollback.
//
// It lives here rather than reusing the disk command's prompt because that one
// is behind a Windows build tag, and undo is not.
func (a *App) confirmUndo(fixID string) bool {
	if a.Stdin == nil {
		return false
	}
	fmt.Fprintf(a.Stdout, "\nRoll back %s? [y/N] ", fixID)
	r := bufio.NewReader(a.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(a.Stdout)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
