//go:build windows

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/host"
	"github.com/wslkit/wslkit/internal/agent/install"
	"github.com/wslkit/wslkit/internal/agent/payload"
	"github.com/wslkit/wslkit/internal/agent/target"
	"github.com/wslkit/wslkit/internal/agent/vmid"
	"github.com/wslkit/wslkit/internal/winapi/hvsock"
)

func (a *App) agentUsage() {
	fmt.Fprint(a.Stderr, `wslkit agent: the guest agent that bridges Windows and WSL 2 distributions.

  wslkit agent install -d <distro> [--autostart]   copy the agent into the distro, enable its systemd unit
  wslkit agent uninstall -d <distro> [--purge]     stop and remove it
  wslkit agent start                               start the Windows-side daemon in the background
  wslkit agent stop                                stop the daemon
  wslkit agent serve                               run the daemon in the foreground (logs to stderr)
  wslkit agent status                              daemon and connected guests
  wslkit agent vm-id                               print the running VM id (passive, does not wake WSL)
  wslkit agent autostart on|off                    start the daemon at logon (per-user Run key)

The daemon listens on a Hyper-V socket bound to the running VM (port `+fmt.Sprint(config.DefaultPort)+`)
and only opens the targets listed in %LOCALAPPDATA%\wslkit\agent\host.json. Guests connect
to it; nothing on the Windows side connects into the VM.
`)
}

func (a *App) agent(args []string) int {
	if len(args) == 0 {
		a.agentUsage()
		return ExitUsage
	}
	switch args[0] {
	case "install":
		return a.agentInstall(args[1:], false)
	case "uninstall":
		return a.agentInstall(args[1:], true)
	case "serve":
		return a.agentServe(args[1:])
	case "start":
		return a.agentStart()
	case "stop":
		return a.agentStop()
	case "status":
		return a.agentStatus()
	case "vm-id":
		id, err := vmid.Passive(context.Background())
		if err != nil {
			fmt.Fprintln(a.Stderr, err)
			return ExitFindings
		}
		fmt.Fprintln(a.Stdout, id)
		return ExitOK
	case "autostart":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			fmt.Fprintln(a.Stderr, "usage: wslkit agent autostart on|off")
			return ExitUsage
		}
		exe, _ := os.Executable()
		if err := install.SetAutostart(args[1] == "on", exe); err != nil {
			fmt.Fprintln(a.Stderr, err)
			return ExitCollector
		}
		fmt.Fprintf(a.Stdout, "autostart %s\n", args[1])
		return ExitOK
	case "help", "--help", "-h":
		a.agentUsage()
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "unknown agent subcommand %q\n\n", args[0])
		a.agentUsage()
		return ExitUsage
	}
}

func (a *App) agentInstall(args []string, uninstall bool) int {
	name := "install"
	if uninstall {
		name = "uninstall"
	}
	fs := flag.NewFlagSet("agent "+name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	distro := fs.String("d", "", "distribution name")
	port := fs.Uint("port", uint(config.DefaultPort), "Hyper-V socket port")
	autostart := fs.Bool("autostart", false, "also start the Windows daemon at logon and now")
	purge := fs.Bool("purge", false, "also remove /etc/wslkit (uninstall)")
	arch := fs.String("arch", "", "guest architecture override (amd64, arm64)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *distro == "" {
		fmt.Fprintf(a.Stderr, "usage: wslkit agent %s -d <distro>\n", name)
		return ExitUsage
	}
	if !uninstall && len(payload.Available()) == 0 {
		fmt.Fprintln(a.Stderr, "this wslkit build has no embedded guest agent; build with `go run ./tools/build-agent` first")
		return ExitCollector
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fmt.Fprintf(a.Stdout, "%s the guest agent in %s (this starts the distribution)\n", strings.ToUpper(name[:1])+name[1:]+"ing", *distro)
	opts := install.Options{Distro: *distro, Port: uint32(*port), Version: a.Version, Arch: *arch, Purge: *purge}
	var rep install.Report
	var err error
	if uninstall {
		rep, err = install.Uninstall(ctx, opts)
	} else {
		rep, err = install.Install(ctx, opts)
	}
	for _, s := range rep.Steps {
		fmt.Fprintln(a.Stdout, "  "+s)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintln(a.Stdout, "  warning: "+w)
	}
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	if !uninstall && *autostart {
		exe, _ := os.Executable()
		if err := install.SetAutostart(true, exe); err != nil {
			fmt.Fprintln(a.Stderr, "autostart:", err)
			return ExitCollector
		}
		fmt.Fprintln(a.Stdout, "  autostart on")
		if code := a.agentStart(); code != ExitOK {
			return code
		}
	}
	if !uninstall {
		fmt.Fprintln(a.Stdout, "Next: wslkit agent start   then   wslkit agent status   (presets: wslkit sock enable ssh-agent -d "+*distro+")")
	}
	return ExitOK
}

// agentServe runs the daemon loop: find the VM, bind, serve until the VM goes
// away, repeat. Ctrl+C stops it.
func (a *App) agentServe(args []string) int {
	fs := flag.NewFlagSet("agent serve", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	quiet := fs.Bool("quiet", false, "log only errors")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	cfg, err := config.LoadHost(filepath.Join(config.HostDir(), "host.json"))
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	logOut := os.Stderr
	logger := log.New(logOut, "wslkit agent: ", log.LstdFlags)
	if *quiet {
		logger.SetOutput(nil)
	}
	d := &host.Daemon{Config: cfg, Version: a.Version, Dial: target.Dial, Logger: logger, StatusFile: host.StatusPath()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() { _ = os.Remove(host.StatusPath()) }()

	wait := 3 * time.Second
	for ctx.Err() == nil {
		id, err := vmid.Passive(ctx)
		if err != nil {
			if !errors.Is(err, vmid.ErrNotRunning) {
				logger.Printf("%v", err)
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			continue
		}
		l, err := hvsock.Listen(id, cfg.Port)
		if err != nil {
			logger.Printf("%v", err)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			continue
		}
		d.SetVMID(id)
		logger.Printf("listening for guests on vm %s port %d (%d allowed target(s))", id, cfg.Port, len(cfg.Allow))
		// Serve until the VM id changes (wsl --shutdown gives the VM a new id and
		// the old listener would never see a guest again) or Accept fails.
		sctx, cancelServe := context.WithCancel(ctx)
		go watchVMID(sctx, id, 15*time.Second, cancelServe)
		err = d.Serve(sctx, l, fmt.Sprintf("hvsock:%s:%d", id, cfg.Port))
		cancelServe()
		if ctx.Err() != nil {
			break
		}
		logger.Printf("listener ended (%v); rediscovering the VM", err)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	return ExitOK
}

// watchVMID cancels when the running VM id differs from bound, or when no VM
// runs any more (the daemon then waits for the next one).
func watchVMID(ctx context.Context, bound string, every time.Duration, cancel func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			id, err := vmid.Passive(ctx)
			switch {
			case err == nil && id == bound:
				misses = 0
			case err == nil && id != bound:
				cancel()
				return
			default:
				// No helper process for a while: the VM is gone; rebind later.
				misses++
				if misses >= 4 {
					cancel()
					return
				}
			}
		}
	}
}

// agentStart spawns `wslkit agent serve --quiet` detached from this console.
func (a *App) agentStart() int {
	if st, ok, _ := host.ReadStatus(); ok && processAlive(st.PID) {
		fmt.Fprintf(a.Stdout, "daemon already running (pid %d)\n", st.PID)
		return ExitOK
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	logPath := filepath.Join(config.HostDir(), "daemon.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	defer logf.Close()
	cmd := exec.Command(exe, "agent", "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008 /*DETACHED_PROCESS*/}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	fmt.Fprintf(a.Stdout, "daemon started (pid %d), log %s\n", cmd.Process.Pid, logPath)
	return ExitOK
}

func (a *App) agentStop() int {
	st, ok, err := host.ReadStatus()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	if !ok || !processAlive(st.PID) {
		fmt.Fprintln(a.Stdout, "daemon is not running")
		_ = os.Remove(host.StatusPath())
		return ExitOK
	}
	p, err := os.FindProcess(st.PID)
	if err == nil {
		err = p.Kill()
	}
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	_ = os.Remove(host.StatusPath())
	fmt.Fprintf(a.Stdout, "stopped daemon (pid %d)\n", st.PID)
	return ExitOK
}

func (a *App) agentStatus() int {
	st, ok, err := host.ReadStatus()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return ExitCollector
	}
	if v, on := install.Autostart(); on {
		fmt.Fprintf(a.Stdout, "autostart: on (%s)\n", v)
	} else {
		fmt.Fprintln(a.Stdout, "autostart: off")
	}
	if !ok || !processAlive(st.PID) {
		fmt.Fprintln(a.Stdout, "daemon: not running   (wslkit agent start)")
		return ExitFindings
	}
	fmt.Fprint(a.Stdout, "daemon: "+st.String())
	if len(st.Sessions) == 0 {
		return ExitFindings
	}
	return ExitOK
}

// processAlive checks a pid with OpenProcess semantics via os.FindProcess.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
