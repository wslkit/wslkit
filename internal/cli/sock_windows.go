//go:build windows

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/host"
	"github.com/wslkit/wslkit/internal/sock"
)

func (a *App) sockUsage() {
	fmt.Fprint(a.Stderr, `wslkit sock: bridge Windows sockets into a WSL 2 distribution.

  wslkit sock list                          available presets and what they bridge
  wslkit sock enable <preset> -d <distro>   wire a preset into a distribution
  wslkit sock disable <preset> -d <distro>  remove it
  wslkit sock status [-d <distro>]          what is enabled, and whether the agent is connected

Presets: `+strings.Join(sock.Names(), ", ")+`

Each preset creates an AF_UNIX socket in the distribution that the wslkit guest
agent relays to a Windows named pipe or agent socket. Requires the agent:
wslkit agent install -d <distro>; wslkit agent start.
`)
}

func (a *App) sock(args []string) int {
	if len(args) == 0 {
		a.sockUsage()
		return ExitUsage
	}
	switch args[0] {
	case "list":
		return a.sockList()
	case "enable", "disable":
		return a.sockToggle(args[0], args[1:])
	case "status":
		return a.sockStatus(args[1:])
	case "help", "--help", "-h":
		a.sockUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown sock subcommand %q\n\n", args[0])
		a.sockUsage()
		return ExitUsage
	}
}

func (a *App) sockList() int {
	for _, p := range sock.All {
		target := p.Target
		if target == "" {
			target = "(discovered from the Windows GnuPG home)"
		}
		fmt.Fprintf(a.Stdout, "%-16s %s\n%-16s target %s\n", p.Name, p.Desc, "", target)
		for k, v := range p.Env {
			fmt.Fprintf(a.Stdout, "%-16s sets   %s=%s\n", "", k, v)
		}
	}
	return ExitOK
}

func (a *App) sockToggle(verb string, args []string) int {
	fs := flag.NewFlagSet("sock "+verb, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	// The preset may come before the flags (`enable ssh-agent -d Ubuntu`), which
	// the flag package would otherwise stop at, or after them.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	}
	if name == "" || *distro == "" || (name != "" && fs.NArg() > 1) {
		fmt.Fprintf(a.Stderr, "usage: wslkit sock %s <preset> -d <distro>\npresets: %s\n", verb, strings.Join(sock.Names(), ", "))
		return ExitUsage
	}
	p, ok := sock.Lookup(name)
	if !ok {
		fmt.Fprintf(a.Stderr, "unknown preset %q; presets: %s\n", name, strings.Join(sock.Names(), ", "))
		return ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var steps []string
	var err error
	if verb == "enable" {
		steps, err = sock.Enable(ctx, *distro, p, sock.HostConfigPath())
	} else {
		steps, err = sock.Disable(ctx, *distro, p)
	}
	for _, s := range steps {
		fmt.Fprintln(a.Stdout, "  "+s)
	}
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	if verb == "enable" {
		for _, n := range p.Notes {
			fmt.Fprintln(a.Stdout, "  note: "+n)
		}
		if st, ok, _ := host.ReadStatus(); !ok || len(st.Sessions) == 0 {
			fmt.Fprintln(a.Stdout, "  the Windows daemon is not serving this distro yet: wslkit agent start")
		}
	}
	return ExitOK
}

func (a *App) sockStatus(args []string) int {
	fs := flag.NewFlagSet("sock status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name (default: every distro with the agent installed)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	st, running, _ := host.ReadStatus()
	if running {
		fmt.Fprint(a.Stdout, "daemon: "+st.String())
	} else {
		fmt.Fprintln(a.Stdout, "daemon: not running   (wslkit agent start)")
	}
	h, err := config.LoadHost(sock.HostConfigPath())
	if err == nil && len(h.Allow) > 0 {
		fmt.Fprintln(a.Stdout, "allowed targets:")
		for _, t := range h.Allow {
			f := h.FilterFor(t)
			if f != "" {
				f = "   filter " + f
			}
			fmt.Fprintf(a.Stdout, "  %s%s\n", t, f)
		}
	}
	if *distro == "" {
		return ExitOK
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ls, err := sock.Enabled(ctx, *distro)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitFindings
	}
	if len(ls) == 0 {
		fmt.Fprintf(a.Stdout, "%s: no presets enabled\n", *distro)
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s:\n", *distro)
	for _, l := range ls {
		fmt.Fprintf(a.Stdout, "  %-16s %s -> %s\n", l.Name, l.Unix, l.Target)
	}
	return ExitOK
}
