//go:build windows

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/wslkit/wslkit/internal/proxy"
	"github.com/wslkit/wslkit/internal/winapi/winhttp"
)

func (a *App) proxyUsage() {
	fmt.Fprint(a.Stderr, `wslkit proxy: get a Windows proxy configuration working inside a distribution.

  wslkit proxy show [--for URL]           what Windows is configured to do, and what a distro would get
  wslkit proxy apply -d <distro>          write it into the distribution, everywhere that reads one
  wslkit proxy revert -d <distro>         take it out again
  wslkit proxy serve                      run a local proxy that asks Windows per request
  wslkit proxy check -d <distro>          can the distribution actually reach it?

show flags:
  --for URL     evaluate a PAC script or WPAD against this URL instead of the
                default. A script can answer differently for every host, so
                there is no single answer to "what is the proxy"
  --pac URL     evaluate this PAC script instead of the configured one, to try
                one out before deploying it
  --http URL    use this proxy instead of what Windows says
  --https URL   the same, for HTTPS
  --json        machine-readable output

apply and revert flags:
  -d DISTRO     the distribution to write into (required)
  --dry-run     show what would be written and change nothing
  -y, --yes     do not prompt
  --timeout D   bound on each command run inside the distribution

serve flags:
  --port N          port to listen on (default 18080)
  --upstream URL    send everything to this proxy instead of asking Windows
  --direct          send everything direct instead of asking Windows
  --pac URL         evaluate this PAC script instead of the configured one
  --cache-ttl D     how long to reuse a resolution for one host (default 1m)
  --loopback-only   do not listen on the WSL gateway address
  --quiet           do not log each request

serve is for the case apply cannot cover: a PAC script that answers differently
per host. It listens on the WSL gateway, asks Windows where each request should
go, and tunnels or forwards it there.

apply writes five files, because each is read by something that reads none of
the others: /etc/wslkit/proxy.env for a unit to watch, /etc/environment for
every login session, a profile.d script for login shells, an apt.conf.d file
because apt does not read the environment from a timer, and a systemd drop-in
because systemd builds its own environment. Each carries a marked block, so
revert removes exactly what was written and leaves your own lines alone.

WSL has its own proxy support, and it stops short in three places this reports
on: it injects the variables per process and writes them to no file, so systemd
units never see them; it passes a PAC script through unevaluated, so a machine
whose proxy comes from a script has none inside the distribution; and in NAT
mode it drops a proxy on the host's loopback rather than rewriting it to the
gateway.
`)
}

func (a *App) proxy(args []string) int {
	if len(args) == 0 {
		a.proxyUsage()
		return ExitUsage
	}
	switch args[0] {
	case "show":
		return a.proxyShow(args[1:])
	case "apply":
		return a.proxyApply(args[1:])
	case "revert":
		return a.proxyRevert(args[1:])
	case "serve":
		return a.proxyServe(args[1:])
	case "check":
		return a.proxyCheck(args[1:])
	case "help", "--help", "-h":
		a.proxyUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown proxy subcommand %q\n\n", args[0])
		a.proxyUsage()
		return ExitUsage
	}
}

// proxyShow reports the three places Windows keeps a proxy setting, what a
// distribution would be given, and what WSL's own support will not do with it.
func (a *App) proxyShow(args []string) int {
	fs := flag.NewFlagSet("proxy show", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOut := fs.Bool("json", false, "machine-readable output")
	forURL := fs.String("for", "", "evaluate a PAC script against this URL")
	pac := fs.String("pac", "", "evaluate this PAC script instead of the configured one")
	httpProxy := fs.String("http", "", "use this proxy instead of what Windows says")
	httpsProxy := fs.String("https", "", "use this proxy for HTTPS instead of what Windows says")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Stderr, "proxy show takes no arguments, got %q\n", fs.Arg(0))
		return ExitUsage
	}

	d := proxy.Discover()
	mode, modeSource := proxy.NetworkingMode()
	ov := proxy.Override{PacURL: *pac, HTTP: *httpProxy, HTTPS: *httpsProxy}
	settings, err := d.ResolveWith(mode, *forURL, ov)
	cfg, _ := d.EffectiveWith(ov)

	if *jsonOut {
		out := map[string]any{
			"networking_mode": mode,
			"nat_gateway":     d.NatGateway,
			"user": map[string]any{
				"auto_detect": d.User.AutoDetect,
				"pac_url":     d.User.PacURL,
				"proxy":       d.User.Proxy,
				"bypass":      d.User.Bypass,
			},
			"machine": map[string]any{
				"proxy":  d.Machine.Proxy,
				"bypass": d.Machine.Bypass,
			},
			"effective": map[string]any{
				"http_proxy":  settings.HTTP,
				"https_proxy": settings.HTTPS,
				"no_proxy":    strings.Join(settings.NoProxy, ","),
				"pac_url":     settings.PacURL,
				"source":      settings.Source,
			},
			"wsl_gaps": proxy.AutoProxyGaps(cfg, mode),
		}
		if err != nil {
			out["error"] = err.Error()
		}
		b, jerr := json.MarshalIndent(out, "", "  ")
		if jerr != nil {
			fmt.Fprintf(a.Stderr, "%v\n", jerr)
			return ExitFindings
		}
		fmt.Fprintln(a.Stdout, string(b))
		return ExitOK
	}

	fmt.Fprintf(a.Stdout, "Windows per-user settings (what a browser and WSL read)\n")
	writeConfig(a.Stdout, d.User, d.UserErr)
	fmt.Fprintf(a.Stdout, "\nMachine-wide WinHTTP settings (netsh winhttp, what services read)\n")
	writeConfig(a.Stdout, d.Machine, d.MachineErr)

	fmt.Fprintf(a.Stdout, "\nHow a distribution reaches the host\n")
	fmt.Fprintf(a.Stdout, "  networkingMode  %s (%s)\n", mode, modeSource)
	if d.NatGateway != "" {
		fmt.Fprintf(a.Stdout, "  NAT gateway     %s", d.NatGateway)
		if d.NatNetwork != "" {
			fmt.Fprintf(a.Stdout, "  in %s", d.NatNetwork)
		}
		fmt.Fprintln(a.Stdout)
	}

	fmt.Fprintf(a.Stdout, "\nWhat a distribution would be given\n")
	switch {
	case err != nil:
		fmt.Fprintf(a.Stdout, "  could not work it out: %v\n", err)
	case settings.Empty():
		fmt.Fprintf(a.Stdout, "  nothing: %s\n", settings.Source)
	default:
		for _, v := range settings.Env() {
			// One spelling each; both are written when it is applied, and
			// printing eight lines to say four things helps nobody.
			if v.Name == strings.ToLower(v.Name) {
				fmt.Fprintf(a.Stdout, "  %-12s %s\n", v.Name, v.Value)
			}
		}
		fmt.Fprintf(a.Stdout, "  source       %s\n", settings.Source)
	}

	if gaps := proxy.AutoProxyGaps(cfg, mode); len(gaps) > 0 && !cfg.Empty() {
		fmt.Fprintf(a.Stdout, "\nWhat WSL's own proxy support will not do here\n")
		for _, g := range gaps {
			fmt.Fprintf(a.Stdout, "  - %s\n", g)
		}
	}
	return ExitOK
}

func writeConfig(w io.Writer, c winhttp.Config, err error) {
	if err != nil {
		fmt.Fprintf(w, "  could not be read: %v\n", err)
		return
	}
	if c.Empty() {
		fmt.Fprintln(w, "  none")
		return
	}
	if c.AutoDetect {
		fmt.Fprintln(w, "  auto-detect     on (WPAD)")
	}
	if c.PacURL != "" {
		fmt.Fprintf(w, "  PAC script      %s\n", c.PacURL)
	}
	if c.Proxy != "" {
		fmt.Fprintf(w, "  proxy           %s\n", c.Proxy)
	}
	if c.Bypass != "" {
		fmt.Fprintf(w, "  bypass          %s\n", c.Bypass)
	}
}
