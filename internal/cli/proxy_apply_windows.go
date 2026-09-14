//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/proxy"
)

// proxyApply writes the proxy configuration into a distribution, in the five
// places that between them cover everything that reads one.
//
// This is the part WSL does not do. It injects the variables into each process
// it starts and writes them to no file, so a systemd unit, a cron job or a
// shell that was already open never sees them.
func (a *App) proxyApply(args []string) int {
	fs := flag.NewFlagSet("proxy apply", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	dryRun := fs.Bool("dry-run", false, "show what would be written and change nothing")
	yes := fs.Bool("y", false, "do not prompt for confirmation")
	fs.BoolVar(yes, "yes", false, "do not prompt for confirmation")
	forURL := fs.String("for", "", "evaluate a PAC script against this URL")
	pac := fs.String("pac", "", "evaluate this PAC script instead of the configured one")
	httpProxy := fs.String("http", "", "use this proxy instead of what Windows says")
	httpsProxy := fs.String("https", "", "use this proxy for HTTPS instead of what Windows says")
	timeout := fs.Duration("timeout", proxy.DefaultTimeout, "bound on each command run inside the distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *distro == "" || fs.NArg() > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit proxy apply -d <distro> [--dry-run] [--http URL]")
		return ExitUsage
	}

	d := proxy.Discover()
	mode, _ := proxy.NetworkingMode()
	settings, err := d.ResolveWith(mode, *forURL, proxy.Override{PacURL: *pac, HTTP: *httpProxy, HTTPS: *httpsProxy})
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	if settings.Empty() {
		fmt.Fprintf(a.Stderr, "there is no proxy to apply: %s\n", settings.Source)
		fmt.Fprintln(a.Stderr, "name one with --http, or see what Windows has with: wslkit proxy show")
		return ExitCollector
	}

	ctx := context.Background()
	changes, err := proxy.PlanApply(ctx, proxy.WSLRunner{}, *distro, settings, *timeout)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}

	fmt.Fprintf(a.Stdout, "%s\n  %s\n\n", *distro, settings.Source)
	for _, v := range settings.Env() {
		if v.Name == strings.ToLower(v.Name) {
			fmt.Fprintf(a.Stdout, "  %-12s %s\n", v.Name, v.Value)
		}
	}
	fmt.Fprintln(a.Stdout)
	a.renderChanges(changes)

	if !anyChanged(changes) {
		fmt.Fprintln(a.Stdout, "nothing to do: the distribution already has exactly this")
		return ExitOK
	}
	if *dryRun {
		fmt.Fprintln(a.Stdout, "--dry-run: nothing was written")
		return ExitOK
	}
	if !*yes && !a.confirm(fmt.Sprintf("write these %d file(s) in %s?", countChanged(changes), *distro)) {
		fmt.Fprintln(a.Stdout, "nothing was written")
		return ExitOK
	}
	if err := proxy.Apply(ctx, proxy.WSLRunner{}, *distro, changes, *timeout); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	fmt.Fprintf(a.Stdout, "\nwritten. New shells have it now.\n")
	fmt.Fprintln(a.Stdout, "For systemd units: wsl --terminate "+*distro+", or systemctl daemon-reexec inside it.")
	fmt.Fprintln(a.Stdout, "Consider setting wsl2.autoProxy=false in .wslconfig, so WSL stops injecting its own.")
	return ExitOK
}

// proxyRevert takes it all out again, leaving the user's own lines alone.
func (a *App) proxyRevert(args []string) int {
	fs := flag.NewFlagSet("proxy revert", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	dryRun := fs.Bool("dry-run", false, "show what would be removed and change nothing")
	yes := fs.Bool("y", false, "do not prompt for confirmation")
	fs.BoolVar(yes, "yes", false, "do not prompt for confirmation")
	timeout := fs.Duration("timeout", proxy.DefaultTimeout, "bound on each command run inside the distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *distro == "" || fs.NArg() > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit proxy revert -d <distro> [--dry-run]")
		return ExitUsage
	}

	ctx := context.Background()
	changes, err := proxy.PlanRevert(ctx, proxy.WSLRunner{}, *distro, *timeout)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	if !anyChanged(changes) {
		fmt.Fprintf(a.Stdout, "%s has no wslkit proxy configuration\n", *distro)
		return ExitOK
	}
	a.renderChanges(changes)
	if *dryRun {
		fmt.Fprintln(a.Stdout, "--dry-run: nothing was removed")
		return ExitOK
	}
	if !*yes && !a.confirm(fmt.Sprintf("remove the proxy configuration from %s?", *distro)) {
		fmt.Fprintln(a.Stdout, "nothing was removed")
		return ExitOK
	}
	if err := proxy.Apply(ctx, proxy.WSLRunner{}, *distro, changes, *timeout); err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	fmt.Fprintln(a.Stdout, "\nremoved. Already-running processes keep the environment they started with.")
	return ExitOK
}

// renderChanges lists what each file is for and what would happen to it, which
// is the only chance anybody gets to object.
func (a *App) renderChanges(changes []proxy.Change) {
	for _, c := range changes {
		verb := "unchanged"
		switch {
		case !c.Changed():
		case c.Removes:
			verb = "remove"
		case !c.Existed:
			verb = "create"
		default:
			verb = "update"
		}
		fmt.Fprintf(a.Stdout, "  %-8s %-44s %s\n", verb, c.File.Path, c.File.Why)
	}
	fmt.Fprintln(a.Stdout)
}

func anyChanged(changes []proxy.Change) bool {
	for _, c := range changes {
		if c.Changed() {
			return true
		}
	}
	return false
}

func countChanged(changes []proxy.Change) int {
	n := 0
	for _, c := range changes {
		if c.Changed() {
			n++
		}
	}
	return n
}
