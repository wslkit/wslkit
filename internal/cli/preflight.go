package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/preflight"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/render"
)

// preflight reads a .wsl file and says what installing it would run into.
//
// The point is the order of events. `wsl --install --from-file` unpacks several
// gigabytes and then reports a mistake in a 200-byte configuration file as an
// HRESULT, and a distribution that installs perfectly can still fail to start
// on this machine's runtime. All of that is readable from the archive's headers
// beforehand, in a second, without writing anything.
func (a *App) preflight(args []string) int {
	fs := flag.NewFlagSet("doctor preflight", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	rf := a.bind(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(a.Stderr, "usage: wslkit doctor preflight [flags] <file.wsl>")
		return ExitUsage
	}
	path := fs.Arg(0)

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(a.Stderr, "cannot read %s: %v\n", path, err)
		return ExitUsage
	}
	defer func() { _ = f.Close() }()

	// Generous, because this reads a whole compressed archive from disk, and
	// bounded, because a corrupt one should not hang a terminal.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	archive, err := preflight.Inspect(ctx, f)
	if err != nil {
		if errors.Is(err, preflight.ErrXZ) || errors.Is(err, preflight.ErrZstd) {
			fmt.Fprintf(a.Stderr, "%v\n\nUnpack it first and check the tar:\n  7z x %s\n", err, path)
			return ExitCollector
		}
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}

	// The machine is read too, because the check that matters most is
	// whether this runtime can start this distribution. A machine that
	// cannot be read is not fatal: the file is still checked, and the
	// runtime check says it was skipped.
	e, _ := a.machineFor(rf)
	if e == nil {
		e = env.New(a.toolName())
	}
	results := preflight.Check(archive, e)

	o := render.Options{Version: a.Version, Redact: !rf.noRedact, Verbose: true}
	switch {
	case rf.jsonOut:
		if err := render.JSON(a.Stdout, e, results, o); err != nil {
			fmt.Fprintf(a.Stderr, "%v\n", err)
			return ExitCollector
		}
	default:
		fmt.Fprintf(a.Stdout, "%s: %s archive, %d entries, %s\n\n",
			path, archive.Compression, archive.Entries, humanBytes(archive.Bytes))
		render.Human(a.Stdout, e, results, o)
	}
	return preflightExit(results)
}

// preflightExit maps the findings to the exit contract. A FAIL here means the
// install would not work, which is worth its own code: this is meant to be run
// from a script before `wsl --install`, and that script needs to be able to
// stop.
func preflightExit(results []probe.Result) int {
	worst := ExitOK
	for _, r := range results {
		switch r.Status {
		case probe.Fail:
			return ExitCollector
		case probe.Warn:
			worst = ExitFindings
		}
	}
	return worst
}

// machineFor collects the live machine, or loads the snapshot if one was given.
// Unlike environment, a failure is not fatal: the file checks stand on their
// own.
func (a *App) machineFor(rf *runFlags) (*env.Env, int) {
	e, code := a.environment(rf)
	if e == nil {
		return nil, code
	}
	return e, ExitOK
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
