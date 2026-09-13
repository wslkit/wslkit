// Package config defines the JSON configuration shared by the Windows daemon
// (allowed targets, presets) and the guest agent (listeners). Pure Go.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultPort is the Hyper-V socket port the daemon listens on and the guest
// agent connects to. WSL's own services use 50000-50006.
const DefaultPort uint32 = 51000

// Target schemes understood by the two sides.
const (
	SchemeNPipe  = "npipe"  // Windows named pipe: npipe:\\.\pipe\name
	SchemeTCP    = "tcp"    // tcp:host:port (Windows side only, must be allow-listed)
	SchemeAssuan = "assuan" // assuan:<path to gpg4win socket file> (port + nonce)
	SchemeUnix   = "unix"   // unix:/path (guest side)
)

// Host is %LOCALAPPDATA%\wslkit\agent\host.json.
type Host struct {
	Port uint32 `json:"port"`
	// Allow lists the targets the guest may open, exactly as they appear in OPEN frames.
	Allow []string `json:"allow"`
	// Filters maps a target to a named traffic filter ("ssh-agent").
	Filters map[string]string `json:"filters,omitempty"`
}

// Listener is one guest-side socket the agent serves.
type Listener struct {
	Name     string `json:"name"`      // preset or user name, e.g. "ssh-agent"
	Unix     string `json:"unix"`      // AF_UNIX path to create in the distro
	Target   string `json:"target"`    // what to ask the host to open
	OwnerUID int    `json:"owner_uid"` // chown the socket; -1 keeps root
	Mode     uint32 `json:"mode"`      // socket mode, e.g. 0600
	Filter   string `json:"filter,omitempty"`
}

// Guest is /etc/wslkit/agent.json.
type Guest struct {
	Port      uint32     `json:"port"`
	Listeners []Listener `json:"listeners"`
}

// ParseTarget splits "scheme:rest" and validates the scheme.
func ParseTarget(t string) (scheme, rest string, err error) {
	i := strings.IndexByte(t, ':')
	if i <= 0 || i == len(t)-1 {
		return "", "", fmt.Errorf("target %q must look like scheme:address", t)
	}
	scheme, rest = strings.ToLower(t[:i]), t[i+1:]
	switch scheme {
	case SchemeNPipe:
		if !strings.HasPrefix(strings.ToLower(rest), `\\.\pipe\`) {
			return "", "", fmt.Errorf("named pipe target must start with \\\\.\\pipe\\: %q", t)
		}
	case SchemeTCP:
		if !strings.Contains(rest, ":") {
			return "", "", fmt.Errorf("tcp target needs host:port: %q", t)
		}
	case SchemeAssuan:
		if rest == "" {
			return "", "", fmt.Errorf("assuan target needs a path: %q", t)
		}
	case SchemeUnix:
		if !strings.HasPrefix(rest, "/") {
			return "", "", fmt.Errorf("unix target needs an absolute path: %q", t)
		}
	default:
		return "", "", fmt.Errorf("unknown target scheme %q", scheme)
	}
	return scheme, rest, nil
}

// Allowed reports whether target is on the host allow list (case-insensitive).
func (h Host) Allowed(target string) bool {
	for _, a := range h.Allow {
		if strings.EqualFold(a, target) {
			return true
		}
	}
	return false
}

// FilterFor returns the filter name for a target, or "".
func (h Host) FilterFor(target string) string {
	for k, v := range h.Filters {
		if strings.EqualFold(k, target) {
			return v
		}
	}
	return ""
}

// Validate checks a host config.
func (h Host) Validate() error {
	if h.Port == 0 {
		return errors.New("host config: port is required")
	}
	for _, a := range h.Allow {
		if _, _, err := ParseTarget(a); err != nil {
			return fmt.Errorf("host config allow: %w", err)
		}
	}
	return nil
}

// Validate checks a guest config.
func (g Guest) Validate() error {
	if g.Port == 0 {
		return errors.New("guest config: port is required")
	}
	seen := map[string]bool{}
	for i, l := range g.Listeners {
		if l.Name == "" || l.Unix == "" {
			return fmt.Errorf("guest config listeners[%d]: name and unix are required", i)
		}
		if !strings.HasPrefix(l.Unix, "/") {
			return fmt.Errorf("guest config listeners[%d]: unix path must be absolute", i)
		}
		if seen[l.Unix] {
			return fmt.Errorf("guest config listeners[%d]: duplicate socket %s", i, l.Unix)
		}
		seen[l.Unix] = true
		if _, _, err := ParseTarget(l.Target); err != nil {
			return fmt.Errorf("guest config listeners[%d]: %w", i, err)
		}
	}
	return nil
}

// DefaultHost returns an empty host config on the default port.
func DefaultHost() Host { return Host{Port: DefaultPort, Filters: map[string]string{}} }

// DefaultGuest returns an empty guest config on the default port.
func DefaultGuest() Guest { return Guest{Port: DefaultPort} }

// LoadHost reads path; a missing file yields DefaultHost.
func LoadHost(path string) (Host, error) {
	h := DefaultHost()
	if err := load(path, &h); err != nil {
		return h, err
	}
	if h.Filters == nil {
		h.Filters = map[string]string{}
	}
	return h, h.Validate()
}

// LoadGuest reads path; a missing file yields DefaultGuest.
func LoadGuest(path string) (Guest, error) {
	g := DefaultGuest()
	if err := load(path, &g); err != nil {
		return g, err
	}
	return g, g.Validate()
}

func load(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Save writes v as indented JSON, creating the directory.
func Save(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// HostDir is where the Windows daemon keeps its config, status and log.
func HostDir() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "wslkit", "agent")
	}
	return filepath.Join(os.TempDir(), "wslkit", "agent")
}

// Guest-side well-known paths.
const (
	GuestBinDir    = "/usr/local/lib/wslkit"
	GuestBinary    = GuestBinDir + "/wslkit-agent"
	GuestConfigDir = "/etc/wslkit"
	GuestConfig    = GuestConfigDir + "/agent.json"
	GuestRunDir    = "/run/wslkit"
	GuestUnit      = "/etc/systemd/system/wslkit-agent.service"
)
