//go:build windows

// Package install puts the guest agent into a distribution and registers it
// with systemd, using wsl.exe as root. Every step is explicit and reversible;
// the caller (the CLI) has already obtained the user's intent, since this
// starts the distribution.
package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/payload"
)

// Options for Install/Uninstall.
type Options struct {
	Distro  string
	Port    uint32
	Version string
	// Arch overrides detection ("amd64", "arm64").
	Arch string
	// Purge also removes /etc/wslkit on uninstall.
	Purge bool
}

// Report is what happened, for the CLI to print.
type Report struct {
	Distro   string
	Arch     string
	Systemd  bool
	Steps    []string
	Warnings []string
}

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runKeyName = "wslkit-agent"

// Install copies the agent, writes its config and unit, and starts it.
func Install(ctx context.Context, o Options) (Report, error) {
	r := Report{Distro: o.Distro}
	if o.Distro == "" {
		return r, errors.New("distro name is required")
	}
	if o.Port == 0 {
		o.Port = config.DefaultPort
	}
	arch := o.Arch
	if arch == "" {
		out, err := run(ctx, o.Distro, nil, "uname", "-m")
		if err != nil {
			return r, fmt.Errorf("cannot query the distro's architecture: %w", err)
		}
		switch m := strings.TrimSpace(out); m {
		case "x86_64":
			arch = "amd64"
		case "aarch64":
			arch = "arm64"
		default:
			return r, fmt.Errorf("unsupported guest architecture %q", m)
		}
	}
	r.Arch = arch
	bin, err := payload.Binary(arch)
	if err != nil {
		return r, err
	}

	// 1. binary
	script := fmt.Sprintf("mkdir -p %s %s %s && cat > %s.tmp && chmod 755 %s.tmp && mv -f %s.tmp %s",
		config.GuestBinDir, config.GuestConfigDir, config.GuestRunDir, config.GuestBinary, config.GuestBinary, config.GuestBinary, config.GuestBinary)
	if _, err := run(ctx, o.Distro, bin, "sh", "-c", script); err != nil {
		return r, fmt.Errorf("copying the agent binary: %w", err)
	}
	r.Steps = append(r.Steps, fmt.Sprintf("installed %s (%d bytes, linux/%s)", config.GuestBinary, len(bin), arch))

	// 2. config: keep existing listeners, set the port
	cfg := config.DefaultGuest()
	if existing, err := run(ctx, o.Distro, nil, "cat", config.GuestConfig); err == nil && strings.TrimSpace(existing) != "" {
		if jerr := json.Unmarshal([]byte(existing), &cfg); jerr != nil {
			r.Warnings = append(r.Warnings, "existing "+config.GuestConfig+" was not valid JSON and has been replaced")
			cfg = config.DefaultGuest()
		}
	}
	cfg.Port = o.Port
	cb, _ := json.MarshalIndent(cfg, "", "  ")
	if _, err := run(ctx, o.Distro, append(cb, '\n'), "sh", "-c", "cat > "+config.GuestConfig+" && chmod 644 "+config.GuestConfig); err != nil {
		return r, fmt.Errorf("writing %s: %w", config.GuestConfig, err)
	}
	r.Steps = append(r.Steps, "wrote "+config.GuestConfig)

	// 3. systemd unit
	if _, err := run(ctx, o.Distro, nil, "test", "-d", "/run/systemd/system"); err != nil {
		r.Warnings = append(r.Warnings, "systemd is not running in this distro (enable it with [boot] systemd=true in /etc/wsl.conf). The agent is installed but not started; run "+config.GuestBinary+" manually or enable systemd and re-run install.")
		return r, nil
	}
	r.Systemd = true
	unit := Unit(o.Distro)
	if _, err := run(ctx, o.Distro, []byte(unit), "sh", "-c", "cat > "+config.GuestUnit+" && systemctl daemon-reload && systemctl enable --now wslkit-agent.service"); err != nil {
		return r, fmt.Errorf("installing the systemd unit: %w", err)
	}
	r.Steps = append(r.Steps, "enabled and started wslkit-agent.service")
	// 4. Windows side: make sure a host config exists.
	hp := filepath.Join(config.HostDir(), "host.json")
	if _, err := os.Stat(hp); errors.Is(err, os.ErrNotExist) {
		h := config.DefaultHost()
		h.Port = o.Port
		if err := config.Save(hp, h); err != nil {
			return r, err
		}
		r.Steps = append(r.Steps, "wrote "+hp)
	}
	return r, nil
}

// Uninstall stops and removes the agent from a distribution.
func Uninstall(ctx context.Context, o Options) (Report, error) {
	r := Report{Distro: o.Distro}
	if o.Distro == "" {
		return r, errors.New("distro name is required")
	}
	if _, err := run(ctx, o.Distro, nil, "test", "-d", "/run/systemd/system"); err == nil {
		_, _ = run(ctx, o.Distro, nil, "sh", "-c", "systemctl disable --now wslkit-agent.service 2>/dev/null; rm -f "+config.GuestUnit+"; systemctl daemon-reload")
		r.Steps = append(r.Steps, "stopped and removed wslkit-agent.service")
	}
	rm := "rm -f " + config.GuestBinary
	if o.Purge {
		rm += " && rm -rf " + config.GuestConfigDir + " " + config.GuestRunDir
	}
	if _, err := run(ctx, o.Distro, nil, "sh", "-c", rm); err != nil {
		return r, fmt.Errorf("removing files: %w", err)
	}
	r.Steps = append(r.Steps, "removed "+config.GuestBinary)
	if o.Purge {
		r.Steps = append(r.Steps, "removed "+config.GuestConfigDir)
	}
	return r, nil
}

// Unit renders the systemd unit for a distribution.
func Unit(distro string) string {
	return fmt.Sprintf(`[Unit]
Description=wslkit guest agent (bridges sockets to the Windows host)
Documentation=https://github.com/wslkit/wslkit
After=network.target

[Service]
ExecStart=%s --name %q
Restart=always
RestartSec=2
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
`, config.GuestBinary, distro)
}

// SetAutostart registers or removes the per-user Run entry that starts the
// Windows daemon at logon. exe is the wslkit binary path.
func SetAutostart(enable bool, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enable {
		err := k.DeleteValue(runKeyName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	return k.SetStringValue(runKeyName, fmt.Sprintf(`"%s" agent start`, exe))
}

// Autostart reports whether the Run entry exists.
func Autostart() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runKeyName)
	if err != nil {
		return "", false
	}
	return v, true
}

// run executes a command as root inside the distro with optional stdin.
func run(ctx context.Context, distro string, stdin []byte, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	full := append([]string{"-d", distro, "-u", "root", "--"}, args...)
	cmd := exec.CommandContext(cctx, "wsl.exe", full...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(strings.ReplaceAll(errb.String(), "\x00", ""))
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("wsl -d %s -u root -- %s: %w: %s", distro, strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}
