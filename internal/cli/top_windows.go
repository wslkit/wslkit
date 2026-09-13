//go:build windows

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/wslkit/wslkit/internal/top"
)

func (a *App) top(args []string) int {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	interval := fs.Duration("interval", 2*time.Second, "gap between the two samples a CPU rate is measured over")
	once := fs.Bool("once", false, "take one sample and report no CPU, rather than waiting")
	timeout := fs.Duration("timeout", top.DefaultTimeout, "bound on each measurement inside a distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	only := fs.Args()

	if *interval < 0 {
		fmt.Fprintln(a.Stderr, "--interval must not be negative")
		return ExitUsage
	}
	o := top.Options{Interval: *interval, Timeout: *timeout, Only: only}
	if *once {
		// A CPU rate needs two samples separated by time. Asked for one,
		// report memory and say nothing about CPU rather than printing a
		// number that means something else.
		o.Interval = 0
	}

	ctx := context.Background()
	report, rates, err := top.Collect(ctx, top.WSLRunner{}, o)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitFindings
	}

	if len(report.Samples) == 0 {
		if *jsonOut {
			if err := writeJSON(a, map[string]any{"distributions": []any{}, "note": "no distributions are running"}); err != nil {
				return ExitFindings
			}
			return ExitOK
		}
		fmt.Fprintln(a.Stdout, "no distributions are running, so the utility VM is not up")
		return ExitOK
	}

	if *jsonOut {
		if err := writeJSON(a, top.JSON(report, rates)); err != nil {
			return ExitFindings
		}
		return ExitOK
	}
	top.Render(a.Stdout, report, rates)
	return ExitOK
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
