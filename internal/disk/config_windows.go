//go:build windows

package disk

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/wslkit/wslkit/internal/wslconfig"
)

// ConfigPath is where the settings file lives.
func ConfigPath(fs FileSystem) (string, error) {
	appdata, err := fs.ExpandEnv(`%APPDATA%`)
	if err != nil || appdata == "" || appdata == `%APPDATA%` {
		return "", fmt.Errorf("%w: %%APPDATA%% is not set, so there is nowhere to keep settings", ErrRefused)
	}
	return joinWindows(joinWindows(appdata, Product), "config.toml"), nil
}

// Product is the directory the kit keeps its files under.
const Product = "wslkit"

// LoadConfig reads the settings.
//
// A missing file is not an error: the defaults are a complete configuration.
// A file that cannot be parsed is an error, and the caller decides whether to
// carry on with the defaults or stop.
func LoadConfig(fs FileSystem) (Config, string, error) {
	path, err := ConfigPath(fs)
	if err != nil {
		return DefaultConfig(), "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), path, nil
		}
		return DefaultConfig(), path, fmt.Errorf("disk: reading %s: %w", path, err)
	}
	c, err := ParseConfig(string(b))
	if err != nil {
		return DefaultConfig(), path, fmt.Errorf("%s: %w", path, err)
	}
	return c, path, nil
}

// SaveConfig writes the settings, creating the directory if needed.
func SaveConfig(fs FileSystem, c Config) (string, error) {
	path, err := ConfigPath(fs)
	if err != nil {
		return "", err
	}
	if err := fs.MkdirAll(DirOf(path)); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, []byte(RenderConfig(c)), 0o600); err != nil {
		return path, fmt.Errorf("disk: writing %s: %w", path, err)
	}
	return path, nil
}

// WslConfigValues reads the disk-related settings out of the user's .wslconfig.
//
// These are read-only here: they belong to WSL, wslkit only reports them
// because they explain how large a new disk will be allowed to grow.
func WslConfigValues(fs FileSystem) map[string]string {
	profile, err := fs.ExpandEnv(`%USERPROFILE%`)
	if err != nil || profile == "" || profile == `%USERPROFILE%` {
		return nil
	}
	b, err := os.ReadFile(joinWindows(profile, ".wslconfig"))
	if err != nil {
		// Absent or unreadable is simply nothing to report.
		return nil
	}
	cfg := wslconfig.Parse(string(b))
	out := map[string]string{}
	for _, key := range []string{"defaultVhdSize", "vhdSize", "swapFile"} {
		if v, ok := cfg.Get("wsl2", key); ok {
			out["wsl2."+key] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Editor is the command `config edit` opens the file with.
//
// %EDITOR% is treated as a command, not a path: it is quoted only when it
// contains a space and names a file that exists, so `code --wait` is passed
// through as written while C:\Program Files\...\editor.exe is quoted.
func Editor(fs FileSystem) string {
	v, err := fs.ExpandEnv(`%EDITOR%`)
	// An unset variable expands to itself, so getting the literal back means
	// there is no editor configured.
	if err != nil || v == "" || v == `%EDITOR%` {
		return "notepad"
	}
	if strings.Contains(v, " ") && fs.Exists(v) {
		return `"` + v + `"`
	}
	return v
}

// OpenEditor launches the editor on the settings file and waits for it.
func OpenEditor(fs FileSystem, path string) error {
	editor := Editor(fs)
	// The editor may carry arguments, so it is split the way a shell would
	// see it rather than treated as one program name.
	parts := splitCommand(editor)
	if len(parts) == 0 {
		return fmt.Errorf("%w: no editor to open %s with", ErrRefused, path)
	}
	args := append(parts[1:], path)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("disk: running %s: %w. Set %%EDITOR%% to something on PATH, or edit %s by hand", editor, err, path)
	}
	return nil
}

// splitCommand splits a command line on spaces, keeping quoted runs together.
func splitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
