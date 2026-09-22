//go:build windows

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/wslkit/wslkit/internal/top"
	"github.com/wslkit/wslkit/internal/winapi/console"
)

func (a *App) top(args []string) int {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	interval := fs.Duration("interval", 2*time.Second, "gap between the two samples a rate is measured over")
	once := fs.Bool("once", false, "take one sample and report no rates, rather than waiting")
	watch := fs.Bool("watch", false, "keep measuring and redraw every interval, until interrupted")
	timeout := fs.Duration("timeout", top.DefaultTimeout, "bound on each measurement inside a distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	only := fs.Args()

	if *interval < 0 {
		fmt.Fprintln(a.Stderr, "--interval must not be negative")
		return ExitUsage
	}
	if *watch && *once {
		fmt.Fprintln(a.Stderr, "--watch and --once ask for opposite things")
		return ExitUsage
	}
	if *watch && *interval == 0 {
		fmt.Fprintln(a.Stderr, "--watch needs an --interval above zero")
		return ExitUsage
	}
	o := top.Options{Interval: *interval, Timeout: *timeout, Only: only}
	if *once {
		// A rate needs two samples separated by time. Asked for one, report
		// what can be read at an instant and say nothing about rates rather
		// than printing a number that means something else.
		o.Interval = 0
	}

	if *watch {
		return a.topWatch(o, *jsonOut)
	}

	report, err := top.Collect(context.Background(), top.WSLRunner{}, o)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitFindings
	}
	return a.topPrint(report, *jsonOut)
}

// topPrint writes one report.
func (a *App) topPrint(report top.Report, jsonOut bool) int {
	if len(report.Samples) == 0 {
		if jsonOut {
			if err := writeJSON(a, map[string]any{"distributions": []any{}, "note": "no distributions are running"}); err != nil {
				return ExitFindings
			}
			return ExitOK
		}
		fmt.Fprintln(a.Stdout, "no distributions are running, so the utility VM is not up")
		return ExitOK
	}
	if jsonOut {
		if err := writeJSON(a, top.JSON(report)); err != nil {
			return ExitFindings
		}
		return ExitOK
	}
	top.Render(a.Stdout, report)
	return ExitOK
}

// topWatch measures until interrupted. Every frame's rates are measured
// against the frame before it, so each sweep is used twice and the display
// refreshes once per interval rather than once per two.
//
// On a console the screen is redrawn in place. Anywhere else, a pipe or a
// file, frames are written one after another: as a blank-line-separated
// report, or with --json one object per line.
func (a *App) topWatch(o top.Options, jsonOut bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	redraw := false
	if f, ok := a.Stdout.(*os.File); ok && !jsonOut {
		redraw = console.EnableVT(f)
	}

	prev, err := top.Sweep(ctx, top.WSLRunner{}, o)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitFindings
	}
	for frame := 0; ; frame++ {
		select {
		case <-time.After(o.Interval):
		case <-ctx.Done():
			return ExitOK
		}
		cur, err := top.Sweep(ctx, top.WSLRunner{}, o)
		if ctx.Err() != nil {
			return ExitOK
		}
		if err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		report := top.WithRates(prev, cur, o.Interval)
		prev = cur

		if jsonOut {
			if a.topPrint(report, true) != ExitOK {
				return ExitFindings
			}
			continue
		}
		// Render to a buffer first, so the screen is cleared and redrawn in
		// one write rather than flickering through an empty frame.
		var b bytes.Buffer
		if redraw {
			b.WriteString("\x1b[H\x1b[2J")
		} else if frame > 0 {
			b.WriteString("\n")
		}
		saved := a.Stdout
		a.Stdout = &b
		a.topPrint(report, false)
		a.Stdout = saved
		if redraw {
			fmt.Fprintf(&b, "\nevery %s, until Ctrl+C\n", o.Interval)
		}
		if _, err := a.Stdout.Write(b.Bytes()); err != nil {
			return ExitFindings
		}
	}
}

// writeJSON prints one object as a line of JSON.
func writeJSON(a *App, o map[string]any) error {
	b, err := json.Marshal(o)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return err
	}
	fmt.Fprintf(a.Stdout, "%s\n", b)
	return nil
}
