//go:build windows

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/proxy"
	"github.com/wslkit/wslkit/internal/winapi/winhttp"
)

// proxyServe runs a forward proxy on the Windows side that asks Windows, per
// request, where that request should go.
//
// `proxy apply` writes one answer into the distribution, which is right until
// the answer depends on the URL. A PAC script exists precisely because it does:
// an internal host goes direct, a build server has its own proxy, the rule
// changes when the laptop moves. Nothing inside a distribution can evaluate one.
// This can, because it is on the side of the machine that has WinHTTP.
func (a *App) proxyServe(args []string) int {
	fs := flag.NewFlagSet("proxy serve", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	port := fs.Int("port", proxy.DefaultServePort, "port to listen on")
	upstream := fs.String("upstream", "", "send everything to this proxy instead of asking Windows")
	direct := fs.Bool("direct", false, "send everything direct instead of asking Windows")
	pac := fs.String("pac", "", "evaluate this PAC script instead of the configured one")
	quiet := fs.Bool("quiet", false, "do not log each request")
	ttl := fs.Duration("cache-ttl", proxy.DefaultResolveTTL, "how long to reuse a resolution for one host")
	loopbackOnly := fs.Bool("loopback-only", false, "do not listen on the WSL gateway address")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Stderr, "proxy serve takes no arguments, got %q\n", fs.Arg(0))
		return ExitUsage
	}
	if *upstream != "" && *direct {
		fmt.Fprintln(a.Stderr, "--upstream and --direct contradict each other")
		return ExitUsage
	}

	d := proxy.Discover()
	mode, _ := proxy.NetworkingMode()

	// Loopback is always bound: it is what mirrored mode uses and what
	// anything on Windows itself can reach. The gateway is what a NAT
	// distribution has to talk to, and is the whole point of running this.
	binds := []string{"127.0.0.1"}
	if !*loopbackOnly && d.NatGateway != "" && !strings.EqualFold(mode, "mirrored") {
		binds = append(binds, d.NatGateway)
	}

	resolver, closeResolver, source, err := a.proxyResolver(d, *upstream, *direct, *pac, *ttl)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	defer closeResolver()

	logf := func(line string) { fmt.Fprintf(a.Stderr, "%s  %s\n", time.Now().Format("15:04:05"), line) }
	if *quiet {
		logf = nil
	}
	srv := proxy.NewServer(proxy.ServeOptions{
		Binds:    binds,
		Port:     *port,
		Resolver: resolver,
		Log:      logf,
	})
	addrs, warnings := srv.Listen()
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %v\n", w)
	}
	if len(addrs) == 0 {
		fmt.Fprintln(a.Stderr, "nothing could be bound, so there is no proxy to run")
		return ExitCollector
	}

	fmt.Fprintf(a.Stdout, "listening on %s\n", strings.Join(addrs, ", "))
	fmt.Fprintf(a.Stdout, "upstream: %s\n\n", source)
	// The address to hand the distribution is the one it can reach, which is
	// not the one a human would guess.
	inside := addrs[0]
	if len(addrs) > 1 {
		inside = addrs[1]
	}
	fmt.Fprintf(a.Stdout, "Point a distribution at it:\n  wslkit proxy apply -d <distro> --http http://%s\n", inside)
	fmt.Fprintf(a.Stdout, "Stop with Ctrl-C. While this is running, leave it running: the distribution\nhas its address written into its files, and nothing answers there once it stops.\n\n")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitFindings
	}
	fmt.Fprintln(a.Stdout, "stopped")
	return ExitOK
}

// proxyResolver builds the thing that decides where each request goes, and a
// sentence describing it for the banner.
func (a *App) proxyResolver(d proxy.Discovery, upstream string, direct bool, pac string, ttl time.Duration) (proxy.Resolver, func(), string, error) {
	noop := func() {}
	switch {
	case direct:
		return proxy.StaticResolver{}, noop, "everything goes direct (--direct)", nil
	case upstream != "":
		return proxy.StaticResolver{Proxy: upstream}, noop, "everything goes to " + upstream + " (--upstream)", nil
	}

	cfg, source := d.Effective()
	if pac != "" {
		cfg = winhttp.Config{PacURL: pac, Bypass: cfg.Bypass}
		source = "the PAC script given on the command line"
	}
	if cfg.Empty() {
		// Nothing configured is not an error: a machine with no proxy still
		// benefits from having somewhere to point a distribution, and the
		// answer will start being interesting the moment it joins a network
		// that has one.
		return proxy.StaticResolver{}, noop, "Windows has no proxy configured, so everything goes direct", nil
	}
	r, err := proxy.NewWindowsResolver(cfg, ttl)
	if err != nil {
		return nil, noop, "", err
	}
	return r, r.Close, "asked per request, from " + source, nil
}

// proxyCheck asks a distribution whether it can actually reach the proxy.
//
// The answer can only be had from inside. Measured on this machine: a listener
// on the WSL gateway answers Windows and is refused from the distribution,
// because the host firewall blocks inbound connections from the WSL subnet by
// default. Testing from Windows proves nothing about the side that matters.
func (a *App) proxyCheck(args []string) int {
	fs := flag.NewFlagSet("proxy check", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	port := fs.Int("port", proxy.DefaultServePort, "the port the proxy is listening on")
	addr := fs.String("addr", "", "the address to test instead of the gateway")
	timeout := fs.Duration("timeout", proxy.DefaultTimeout, "bound on the command run inside the distribution")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *distro == "" || fs.NArg() > 0 {
		fmt.Fprintln(a.Stderr, "usage: wslkit proxy check -d <distro> [--port N]")
		return ExitUsage
	}

	d := proxy.Discover()
	mode, _ := proxy.NetworkingMode()
	target := *addr
	if target == "" {
		host, why, err := proxy.HostAddress(mode, d.NatGateway)
		if err != nil {
			fmt.Fprintf(a.Stderr, "%v\n", err)
			return ExitCollector
		}
		fmt.Fprintf(a.Stdout, "%s\n", why)
		target = net.JoinHostPort(host, fmt.Sprint(*port))
	}

	ok, msg, err := proxy.CheckReachable(context.Background(), proxy.WSLRunner{}, *distro, target, *timeout)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%v\n", err)
		return ExitCollector
	}
	fmt.Fprintf(a.Stdout, "%s: %s\n", *distro, msg)
	if ok {
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "\nNothing is listening there, or the host firewall is refusing the connection.\n"+
		"Windows blocks inbound connections from the WSL subnet by default, and a proxy\n"+
		"that answers when tested from Windows is still refused from inside a distribution.\n\n"+
		"Start the proxy:\n  wslkit proxy serve --port %d\n\nAllow it through, from an elevated PowerShell:\n  %s\n",
		*port, proxy.FirewallRule(*port, d.NatNetwork))
	return ExitFindings
}
