//go:build windows

package sock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
)

// ProfileScript is the profile.d file the presets' environment lands in.
const ProfileScript = "/etc/profile.d/99-wslkit-sock.sh"

// GuestUser describes the distro's default user, needed for socket ownership
// and $XDG_RUNTIME_DIR.
type GuestUser struct {
	UID        int
	Name       string
	RuntimeDir string
}

// Enable wires a preset into a distro: it adds the guest listener, adds the
// target to the Windows allow list, writes the profile.d exports, and restarts
// the guest agent. It returns the steps taken, for the CLI to print.
func Enable(ctx context.Context, distro string, p Preset, hostPath string) ([]string, error) {
	target, warn, err := Discover(p)
	if err != nil {
		return nil, err
	}
	var warnings []string
	if warn != "" {
		warnings = append(warnings, "warning: "+warn)
	}
	u, err := User(ctx, distro)
	if err != nil {
		return nil, err
	}
	var steps []string

	// 1. Windows allow list and filter.
	h, err := config.LoadHost(hostPath)
	if err != nil {
		return nil, err
	}
	if !h.Allowed(target) {
		h.Allow = append(h.Allow, target)
		steps = append(steps, "allowed "+target+" on the Windows side")
	}
	if p.Filter != "" {
		if h.Filters == nil {
			h.Filters = map[string]string{}
		}
		h.Filters[target] = p.Filter
	}
	if err := config.Save(hostPath, h); err != nil {
		return nil, err
	}

	// 2. Guest listener.
	g, err := guestConfig(ctx, distro)
	if err != nil {
		return nil, err
	}
	l := p.Listener(target, u.UID, u.RuntimeDir)
	replaced := false
	for i := range g.Listeners {
		if strings.EqualFold(g.Listeners[i].Name, p.Name) {
			g.Listeners[i] = l
			replaced = true
		}
	}
	if !replaced {
		g.Listeners = append(g.Listeners, l)
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	if err := writeGuestConfig(ctx, distro, g); err != nil {
		return nil, err
	}
	steps = append(steps, fmt.Sprintf("listening at %s in %s", l.Unix, distro))

	// 3. profile.d exports for every enabled preset that has any.
	if err := writeProfile(ctx, distro, g, u); err != nil {
		return nil, err
	}
	if len(p.Env) > 0 {
		steps = append(steps, "wrote "+ProfileScript+" (open a new shell to pick it up)")
	}

	// 4. Restart the agent so it serves the new listener.
	if err := restartAgent(ctx, distro); err != nil {
		return steps, err
	}
	steps = append(steps, "restarted wslkit-agent in "+distro)
	return append(steps, warnings...), nil
}

// Disable removes a preset from a distro (the Windows allow entry stays unless
// no other distro uses it, which the caller decides).
func Disable(ctx context.Context, distro string, p Preset) ([]string, error) {
	g, err := guestConfig(ctx, distro)
	if err != nil {
		return nil, err
	}
	var kept []config.Listener
	var removed *config.Listener
	for i := range g.Listeners {
		if strings.EqualFold(g.Listeners[i].Name, p.Name) {
			removed = &g.Listeners[i]
			continue
		}
		kept = append(kept, g.Listeners[i])
	}
	if removed == nil {
		return nil, fmt.Errorf("%s is not enabled in %s", p.Name, distro)
	}
	g.Listeners = kept
	if err := writeGuestConfig(ctx, distro, g); err != nil {
		return nil, err
	}
	u, err := User(ctx, distro)
	if err != nil {
		return nil, err
	}
	if err := writeProfile(ctx, distro, g, u); err != nil {
		return nil, err
	}
	_, _ = runRoot(ctx, distro, nil, "rm", "-f", removed.Unix)
	steps := []string{"removed the listener and " + removed.Unix}
	if err := restartAgent(ctx, distro); err != nil {
		return steps, err
	}
	return append(steps, "restarted wslkit-agent in "+distro), nil
}

// Enabled lists the presets currently wired into a distro.
func Enabled(ctx context.Context, distro string) ([]config.Listener, error) {
	g, err := guestConfig(ctx, distro)
	if err != nil {
		return nil, err
	}
	return g.Listeners, nil
}

// User reads the distro's default user, uid and runtime directory.
func User(ctx context.Context, distro string) (GuestUser, error) {
	out, err := runRoot(ctx, distro, nil, "sh", "-c", `id -u; id -un; echo "${XDG_RUNTIME_DIR:-}"`)
	if err != nil {
		return GuestUser{}, err
	}
	// Running as root gives root's ids; ask wsl.exe for the default user instead.
	dout, derr := run(ctx, distro, "sh", "-c", `id -u; id -un; echo "${XDG_RUNTIME_DIR:-}"`)
	if derr == nil {
		out = dout
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(out), "\r", ""), "\n")
	if len(lines) < 2 {
		return GuestUser{}, fmt.Errorf("unexpected id output %q", out)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return GuestUser{}, fmt.Errorf("cannot parse uid from %q", lines[0])
	}
	u := GuestUser{UID: uid, Name: strings.TrimSpace(lines[1])}
	if len(lines) > 2 {
		u.RuntimeDir = strings.TrimSpace(lines[2])
	}
	if u.RuntimeDir == "" {
		u.RuntimeDir = fmt.Sprintf("/run/user/%d", uid)
	}
	return u, nil
}

func guestConfig(ctx context.Context, distro string) (config.Guest, error) {
	g := config.DefaultGuest()
	out, err := runRoot(ctx, distro, nil, "sh", "-c", "cat "+config.GuestConfig+" 2>/dev/null || true")
	if err != nil {
		return g, err
	}
	if strings.TrimSpace(out) == "" {
		return g, fmt.Errorf("the wslkit agent is not installed in this distribution: run `wslkit agent install -d <distro>` first")
	}
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		return g, fmt.Errorf("%s: %w", config.GuestConfig, err)
	}
	if g.Port == 0 {
		g.Port = config.DefaultPort
	}
	return g, nil
}

func writeGuestConfig(ctx context.Context, distro string, g config.Guest) error {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	_, err = runRoot(ctx, distro, append(b, '\n'), "sh", "-c", "cat > "+config.GuestConfig+" && chmod 644 "+config.GuestConfig)
	return err
}

// profileBody renders the profile.d script for the enabled listeners. It
// returns "" when no enabled preset exports anything.
func profileBody(g config.Guest, u GuestUser) string {
	var sb strings.Builder
	any := false
	for _, l := range g.Listeners {
		p, ok := Lookup(l.Name)
		if !ok || len(p.Env) == 0 {
			continue
		}
		any = true
		sb.WriteString("# " + p.Name + "\n")
		// Point every variable at the listener's actual path, which can differ
		// from the preset default (a different uid or runtime directory).
		keys := make([]string, 0, len(p.Env))
		for k := range p.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&sb, "export %s=%s\n", k, l.Unix)
		}
	}
	if !any {
		return ""
	}
	return "# Managed by wslkit; edits are overwritten by `wslkit sock enable/disable`.\n" + sb.String()
}

// writeProfile regenerates the profile.d script from the enabled listeners.
func writeProfile(ctx context.Context, distro string, g config.Guest, u GuestUser) error {
	body := profileBody(g, u)
	if body == "" {
		_, err := runRoot(ctx, distro, nil, "rm", "-f", ProfileScript)
		return err
	}
	_, err := runRoot(ctx, distro, []byte(body), "sh", "-c", "cat > "+ProfileScript+" && chmod 644 "+ProfileScript)
	return err
}

func restartAgent(ctx context.Context, distro string) error {
	if _, err := runRoot(ctx, distro, nil, "test", "-d", "/run/systemd/system"); err != nil {
		return fmt.Errorf("systemd is not running in %s, so the agent cannot be restarted automatically; start %s manually", distro, config.GuestBinary)
	}
	_, err := runRoot(ctx, distro, nil, "systemctl", "restart", "wslkit-agent.service")
	return err
}

// HostConfigPath is the Windows-side allow list.
func HostConfigPath() string { return filepath.Join(config.HostDir(), "host.json") }

func runRoot(ctx context.Context, distro string, stdin []byte, args ...string) (string, error) {
	return wsl(ctx, append([]string{"-d", distro, "-u", "root", "--"}, args...), stdin)
}

func run(ctx context.Context, distro string, args ...string) (string, error) {
	return wsl(ctx, append([]string{"-d", distro, "--"}, args...), nil)
}

func wsl(ctx context.Context, args []string, stdin []byte) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "wsl.exe", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(strings.ReplaceAll(errb.String(), "\x00", ""))
		return out.String(), fmt.Errorf("wsl %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return strings.ReplaceAll(out.String(), "\x00", ""), nil
}
