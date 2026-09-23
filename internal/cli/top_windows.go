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
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/wslkit/wslkit/internal/top"
	"github.com/wslkit/wslkit/internal/winapi/console"
)

func (a *App) top(args []string) int {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	interval := fs.Duration("interval", 2*time.Second, "gap between the samples a rate is measured over; 0 is one instant sample with no rates")
	once := fs.Bool("once", false, "print one report and exit, instead of refreshing on a console")
	watch := fs.Bool("watch", false, "keep refreshing every interval even when not on a console")
	timeout := fs.Duration("timeout", top.DefaultTimeout, "bound on each measurement inside a distribution")
	raw := fs.Bool("raw", false, "print what the measurement printed inside each distribution, unparsed, for a bug report")
	wsl := fs.Bool("wsl", false, "show the WSL section: the utility VM and its distributions (default: both sections)")
	wslc := fs.Bool("wslc", false, "show the wslc section: running wslc session VMs; never starts one (default: both sections)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	only := fs.Args()

	if *interval < 0 {
		fmt.Fprintln(a.Stderr, "--interval must not be negative")
		return ExitUsage
	}
	o := top.Options{Interval: *interval, Timeout: *timeout, Only: only, Sections: top.Sections{WSL: *wsl, WSLC: *wslc}}
	if *raw {
		return a.topRaw(o)
	}

	onConsole := false
	if f, ok := a.Stdout.(*os.File); ok {
		onConsole = console.IsConsole(f)
	}
	mode, err := chooseTopMode(*once, *watch, *jsonOut, onConsole, *interval)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitUsage
	}
	if mode == topWatch {
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
	if redraw {
		// The alternate screen, as top and htop use: frames replace each
		// other instead of piling up in the scrollback, and leaving it puts
		// back whatever was on the screen before.
		fmt.Fprint(a.Stdout, "\x1b[?1049h")
		defer fmt.Fprint(a.Stdout, "\x1b[?1049l")
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
		var frameBuf bytes.Buffer
		a.Stdout = &frameBuf
		a.topPrint(report, false)
		a.Stdout = saved
		if redraw {
			footer := fmt.Sprintf("every %s, until Ctrl+C", o.Interval)
			cols, rows := 0, 0
			if f, ok := a.Stdout.(*os.File); ok {
				cols, rows = console.Size(f)
			}
			b.WriteString(fitFrame(frameBuf.String(), cols, rows, footer))
		} else {
			b.Write(frameBuf.Bytes())
		}
		if _, err := a.Stdout.Write(b.Bytes()); err != nil {
			return ExitFindings
		}
	}
}

// topRaw prints the measurement script's own output for each running
// distribution, unparsed, under a line naming it and the WSL version.
//
// It is what a test fixture is made from, and what to attach to a report
// when top's numbers look wrong on a WSL nobody here runs: the parser can
// then be tested against exactly what that machine printed.
func (a *App) topRaw(o top.Options) int {
	ctx := context.Background()
	r := top.WSLRunner{}
	names, err := r.Running(ctx)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitFindings
	}
	if len(o.Only) > 0 {
		var keep []string
		for _, n := range names {
			for _, w := range o.Only {
				if strings.EqualFold(n, w) {
					keep = append(keep, n)
				}
			}
		}
		names = keep
	}
	if len(names) == 0 {
		fmt.Fprintln(a.Stderr, "no distributions are running")
		return ExitFindings
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = top.DefaultTimeout
	}
	failed := false
	for _, n := range names {
		out, err := r.Sample(ctx, n, timeout)
		fmt.Fprintf(a.Stdout, "# distribution=%s wsl=%s\n", n, wslVersionLine())
		fmt.Fprint(a.Stdout, strings.ReplaceAll(out, "\r\n", "\n"))
		if err != nil {
			fmt.Fprintf(a.Stdout, "# error: %v\n", err)
			failed = true
		}
	}
	if failed {
		return ExitFindings
	}
	return ExitOK
}

// wslVersionLine is the installed WSL version, for labelling a capture, or
// "unknown". It is the MSI's own record, which is where doctor reads it too.
func wslVersionLine() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss\MSI`, registry.READ)
	if err != nil {
		return "unknown"
	}
	defer k.Close()
	if v, _, err := k.GetStringValue("Version"); err == nil && v != "" {
		return v
	}
	return "unknown"
}

// fitFrame cuts a frame to the console's height, so a refreshing display
// never scrolls its own header away, and ends it with the footer. The cut
// is said, not silent. Zero rows means the height is unknown, and nothing is
// cut.
//
// Rows are counted as the console lays them out: a line wider than the window
// wraps, and takes as many rows as it wraps to. The long notes under a table
// do, and counting them as one row each let a frame that looked short enough
// scroll its header off a 30-row window.
func fitFrame(frame string, cols, rows int, footer string) string {
	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	height := func(line string) int {
		n := len([]rune(line))
		if cols <= 0 || n <= cols {
			return 1
		}
		return (n + cols - 1) / cols
	}
	// The footer and the blank line above it, and one row spare, because the
	// last line's newline would otherwise scroll the window by one.
	room := rows - 3
	if rows <= 0 || room <= 1 {
		return strings.Join(lines, "\n") + "\n\n" + footer + "\n"
	}
	used := 0
	for i, line := range lines {
		if used+height(line) > room {
			// Keep a row for saying so; drop lines until it fits.
			for i > 0 && used+1 > room {
				i--
				used -= height(lines[i])
			}
			hidden := len(lines) - i
			lines = append(lines[:i:i], fmt.Sprintf("... %d more line(s); a larger window shows them", hidden))
			break
		}
		used += height(line)
	}
	return strings.Join(lines, "\n") + "\n\n" + footer + "\n"
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
