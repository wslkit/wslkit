// Package sock defines the socket-bridge presets: named pairs of a guest
// AF_UNIX socket and a Windows target, plus how each is wired into a distro.
// Pure Go: discovery of Windows-side paths lives in preset_windows.go.
package sock

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wslkit/wslkit/internal/agent/config"
)

// Preset is one bridged service.
type Preset struct {
	Name string
	// Desc is one line for `wslkit sock list`.
	Desc string
	// Target is the Windows endpoint (see config target schemes). For presets
	// whose path is discovered at enable time it is empty and Discover fills it.
	Target string
	// Unix is the guest socket path; %r expands to $XDG_RUNTIME_DIR (falling
	// back to /run/user/<uid>) and %u to the uid.
	Unix string
	// Env is what the user must export for the preset to be used, if anything.
	Env map[string]string
	// Filter names a traffic filter applied by the Windows daemon.
	Filter string
	// Notes are printed after enabling.
	Notes []string
}

// All presets, in listing order.
var All = []Preset{
	{
		Name:   "ssh-agent",
		Desc:   "use the Windows OpenSSH or 1Password agent from Linux",
		Target: `npipe:\\.\pipe\openssh-ssh-agent`,
		Unix:   "%r/wslkit/ssh-agent.sock",
		Env:    map[string]string{"SSH_AUTH_SOCK": "%r/wslkit/ssh-agent.sock"},
		Filter: "ssh-agent",
		Notes: []string{
			"Windows OpenSSH and 1Password serve the same pipe; only one can own it at a time.",
			"Check with: ssh-add -l",
		},
	},
	{
		Name: "gpg-agent",
		Desc: "use the Windows gpg-agent (gpg4win) from Linux",
		Unix: "%r/gnupg/S.gpg-agent",
		Notes: []string{
			"Run `gpgconf --create-socketdir` in the distro if the socket directory is missing.",
			"The Windows agent must be running: gpg-connect-agent /bye",
		},
	},
	{
		Name: "gpg-agent-ssh",
		Desc: "use gpg4win's SSH support (smartcards, YubiKey) as the SSH agent",
		Unix: "%r/gnupg/S.gpg-agent.ssh",
		Env:  map[string]string{"SSH_AUTH_SOCK": "%r/gnupg/S.gpg-agent.ssh"},
		Notes: []string{
			"Enable `enable-win32-openssh-support` in the Windows gpg-agent.conf.",
			"Do not enable this together with ssh-agent: both set SSH_AUTH_SOCK.",
		},
	},
	{
		Name: "gpg-agent-extra",
		Desc: "use the Windows gpg-agent restricted (extra) socket, for forwarding",
		Unix: "%r/gnupg/S.gpg-agent.extra",
	},
}

// Lookup finds a preset by name.
func Lookup(name string) (Preset, bool) {
	for _, p := range All {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Preset{}, false
}

// Names lists preset names in order.
func Names() []string {
	out := make([]string, 0, len(All))
	for _, p := range All {
		out = append(out, p.Name)
	}
	return out
}

// Expand substitutes %r and %u in a guest path.
func Expand(path string, uid int, runtimeDir string) string {
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", uid)
	}
	r := strings.NewReplacer("%r", runtimeDir, "%u", fmt.Sprint(uid))
	return r.Replace(path)
}

// Listener builds the guest listener entry for a preset.
func (p Preset) Listener(target string, uid int, runtimeDir string) config.Listener {
	return config.Listener{
		Name:     p.Name,
		Unix:     Expand(p.Unix, uid, runtimeDir),
		Target:   target,
		OwnerUID: uid,
		Mode:     0o600,
		Filter:   p.Filter,
	}
}

// EnvLines renders the profile.d exports for a preset, sorted for stability.
func (p Preset) EnvLines(uid int, runtimeDir string) []string {
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("export %s=%s", k, Expand(p.Env[k], uid, runtimeDir)))
	}
	return out
}
