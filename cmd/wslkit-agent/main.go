//go:build linux

// wslkit-agent runs inside a WSL 2 distribution. It connects to the wslkit
// daemon on the Windows host over AF_VSOCK and serves the sockets listed in
// /etc/wslkit/agent.json. Installed and started by `wslkit agent install`.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/guest"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", config.GuestConfig, "agent configuration file")
	name := flag.String("name", "", "distribution name to announce (default: $WSL_DISTRO_NAME, then hostname)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("wslkit-agent", version)
		return
	}
	cfg, err := config.LoadGuest(*cfgPath)
	if err != nil {
		log.Fatalf("wslkit-agent: %v", err)
	}
	if *name == "" {
		*name = os.Getenv("WSL_DISTRO_NAME")
	}
	if *name == "" {
		if h, err := os.Hostname(); err == nil {
			*name = h
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := &guest.Agent{
		Config:  cfg,
		Name:    strings.TrimSpace(*name),
		Version: version,
		Connect: guest.VsockTransport(cfg.Port),
		Logger:  log.New(os.Stderr, "wslkit-agent: ", log.LstdFlags),
	}
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("wslkit-agent: %v", err)
	}
}
