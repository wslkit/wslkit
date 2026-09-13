package disk

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Config holds the settings `wslkit disk` reads from a file. Every one of them
// is a default that the matching command-line flag still overrides.
type Config struct {
	// ScanDirs are extra roots for orphans to search, on top of the
	// built-in three.
	ScanDirs []string
	// CompactTrim runs fstrim before compacting.
	CompactTrim bool
	// CompactRestart starts a distribution again after compacting it, if it
	// was running.
	CompactRestart bool
	// UnlockTimeoutSeconds is how long compact waits for the utility VM to
	// release a disk.
	UnlockTimeoutSeconds uint32
}

// DefaultConfig is a complete configuration. A missing file is not an error,
// because these defaults are what the commands would do anyway.
func DefaultConfig() Config {
	return Config{
		CompactTrim:          true,
		CompactRestart:       false,
		UnlockTimeoutSeconds: uint32(DefaultUnlockTimeout.Seconds()),
	}
}

// The settings, in the order they are printed.
const (
	KeyScanDirs       = "scan.dirs"
	KeyCompactTrim    = "compact.trim"
	KeyCompactRestart = "compact.restart"
	KeyUnlockTimeout  = "wsl.unlock_timeout_seconds"
)

// ConfigKeys lists the settings in display order.
func ConfigKeys() []string {
	return []string{KeyScanDirs, KeyCompactTrim, KeyCompactRestart, KeyUnlockTimeout}
}

// ConfigValue renders one setting as the string `config get` prints. Every
// value is a string, including the booleans and the number, so a caller can
// read them all the same way.
func ConfigValue(c Config, key string) (string, bool) {
	switch key {
	case KeyScanDirs:
		return strings.Join(c.ScanDirs, ";"), true
	case KeyCompactTrim:
		return strconv.FormatBool(c.CompactTrim), true
	case KeyCompactRestart:
		return strconv.FormatBool(c.CompactRestart), true
	case KeyUnlockTimeout:
		return strconv.FormatUint(uint64(c.UnlockTimeoutSeconds), 10), true
	default:
		return "", false
	}
}

// UnknownSettingError explains a key that is not a setting, and lists the ones
// that are, so the user does not need a second command to find out.
func UnknownSettingError(key string) error {
	return fmt.Errorf("%w: there is no setting called %q. The settings are: %s",
		ErrRefused, key, strings.Join(ConfigKeys(), ", "))
}

// SetConfigValue parses a value and stores it.
func SetConfigValue(c *Config, key, value string) error {
	switch key {
	case KeyScanDirs:
		// Semicolon-separated, not comma: a Windows path may contain a
		// comma but never a semicolon.
		var dirs []string
		for _, part := range strings.Split(value, ";") {
			if p := strings.TrimSpace(part); p != "" {
				dirs = append(dirs, p)
			}
		}
		c.ScanDirs = dirs
		return nil
	case KeyCompactTrim, KeyCompactRestart:
		// Only true and false. Accepting 1, yes and on would mean
		// guessing at what the file should say when it is written back.
		b, err := parseStrictBool(value)
		if err != nil {
			return fmt.Errorf("%w: %s takes true or false, not %q", ErrRefused, key, value)
		}
		if key == KeyCompactTrim {
			c.CompactTrim = b
		} else {
			c.CompactRestart = b
		}
		return nil
	case KeyUnlockTimeout:
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
		if err != nil {
			return fmt.Errorf("%w: %s takes a whole number of seconds, not %q", ErrRefused, key, value)
		}
		if n > uint64(MaxUnlockTimeout.Seconds()) {
			return fmt.Errorf("%w: %s is at most %d seconds", ErrRefused, key, int(MaxUnlockTimeout.Seconds()))
		}
		c.UnlockTimeoutSeconds = uint32(n)
		return nil
	default:
		return UnknownSettingError(key)
	}
}

func parseStrictBool(s string) (bool, error) {
	switch strings.TrimSpace(s) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("not a boolean")
	}
}

// ParseConfig reads the settings file.
//
// The parser handles what RenderConfig writes plus the obvious hand edits:
// comments, blank lines, section headers, and the four value shapes. It is not
// a general TOML implementation and does not try to be; the dependency
// allow-list has no TOML parser and this file is ours to shape.
//
// Unknown keys are ignored rather than rejected, so a file written by a later
// version still loads.
func ParseConfig(text string) (Config, error) {
	c := DefaultConfig()
	section := ""
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := i + 1
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "[") {
			if !strings.HasSuffix(s, "]") {
				return c, fmt.Errorf("%w: line %d is not a valid section header: %q", ErrRefused, line, s)
			}
			section = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		eq := strings.Index(s, "=")
		if eq < 0 {
			return c, fmt.Errorf("%w: line %d is neither a setting nor a comment: %q", ErrRefused, line, s)
		}
		name := strings.TrimSpace(s[:eq])
		value := strings.TrimSpace(s[eq+1:])
		key := name
		if section != "" {
			key = section + "." + name
		}
		if _, known := ConfigValue(c, key); !known {
			// Forward compatibility: a setting this version does not
			// have is not an error.
			continue
		}
		parsed, err := parseTOMLValue(value)
		if err != nil {
			return c, fmt.Errorf("%w: line %d: %s is %v", ErrRefused, line, key, err)
		}
		if err := SetConfigValue(&c, key, parsed); err != nil {
			return c, fmt.Errorf("%w (line %d)", err, line)
		}
	}
	return c, nil
}

// parseTOMLValue turns the written form of a value back into the string
// SetConfigValue understands.
func parseTOMLValue(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, "["):
		if !strings.HasSuffix(v, "]") {
			return "", fmt.Errorf("an unterminated list")
		}
		inner := strings.TrimSpace(v[1 : len(v)-1])
		if inner == "" {
			return "", nil
		}
		var parts []string
		for _, item := range splitTOMLList(inner) {
			item = strings.TrimSpace(item)
			s, err := unquote(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ";"), nil
	case strings.HasPrefix(v, `"`):
		return unquote(v)
	default:
		return v, nil
	}
}

// splitTOMLList splits on commas that are not inside a quoted string.
func splitTOMLList(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && inQuote:
			cur.WriteRune(r)
			escaped = true
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func unquote(s string) (string, error) {
	if len(s) < 2 || !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) {
		return "", fmt.Errorf("not a quoted string: %q", s)
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
			b.WriteByte(body[i])
			continue
		}
		b.WriteByte(body[i])
	}
	return b.String(), nil
}

// quote writes a TOML basic string, escaping what would end it. A Windows path
// is full of backslashes, so this is not optional.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '"':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// RenderConfig writes the settings file.
//
// The file is regenerated from the parsed values rather than edited in place,
// so a `set` can never corrupt it and the comments explaining each setting stay
// accurate.
func RenderConfig(c Config) string {
	var b strings.Builder
	b.WriteString("# wslkit disk settings. Every value here is a default that the\n")
	b.WriteString("# matching command-line flag still overrides.\n")
	b.WriteString("#\n")
	b.WriteString("# Written by `wslkit disk config set`. Editing it by hand is fine.\n\n")

	b.WriteString("[scan]\n")
	b.WriteString("# Extra roots for `wslkit disk orphans` to search, on top of the\n")
	b.WriteString("# built-in three.\n")
	b.WriteString("dirs = [")
	for i, d := range c.ScanDirs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quote(d))
	}
	b.WriteString("]\n\n")

	b.WriteString("[compact]\n")
	b.WriteString("# Run fstrim before compacting. --no-trim overrides this.\n")
	fmt.Fprintf(&b, "trim = %t\n", c.CompactTrim)
	b.WriteString("# Start the distribution again afterwards if it was running.\n")
	fmt.Fprintf(&b, "restart = %t\n\n", c.CompactRestart)

	b.WriteString("[wsl]\n")
	b.WriteString("# How long `compact` waits for the utility VM to release the disk\n")
	b.WriteString("# after the distribution stops.\n")
	fmt.Fprintf(&b, "unlock_timeout_seconds = %d\n", c.UnlockTimeoutSeconds)
	return b.String()
}

// ConfigJSON is the object printed by `config` with no verb.
func ConfigJSON(path string, c Config, wsl map[string]string) map[string]any {
	settings := map[string]any{}
	for _, k := range ConfigKeys() {
		v, _ := ConfigValue(c, k)
		settings[k] = v
	}
	o := map[string]any{"path": path, "settings": settings}
	if len(wsl) > 0 {
		keys := make([]string, 0, len(wsl))
		for k := range wsl {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := map[string]any{}
		for _, k := range keys {
			m[k] = wsl[k]
		}
		o["wslconfig"] = m
	}
	return o
}
