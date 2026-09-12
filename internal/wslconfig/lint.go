package wslconfig

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/wslver"
)

// Severity of a lint finding.
type Severity string

const (
	Info Severity = "info" // WSL accepts it; worth knowing
	Warn Severity = "warn" // WSL silently ignores it or it will not do what the user meant
	Fail Severity = "fail" // WSL will fail to start the VM or the distro
)

// Finding is one lint result. Line is 1-based; 0 means file-level.
type Finding struct {
	Line     Severity `json:"-"`
	Severity Severity `json:"severity"`
	LineNo   int      `json:"line"`
	Key      string   `json:"key,omitempty"` // section.key
	Message  string   `json:"message"`
	// CommentOut marks lines `fix wslconfig` may disable.
	CommentOut bool `json:"comment_out,omitempty"`
}

// Host facts the linter needs; all optional (zero = unknown, rule skipped).
type Host struct {
	WindowsBuild int
	Runtime      wslver.Version
	TotalRAM     uint64
	// PathExists answers for path-typed values; nil disables path checks.
	PathExists func(string) bool
}

// Lint checks a parsed .wslconfig against the key table and host facts.
func Lint(cfg *Config, table *data.ConfigKeys, host Host) []Finding {
	var out []Finding
	add := func(sev Severity, line int, key, msg string, commentOut bool) {
		out = append(out, Finding{Severity: sev, LineNo: line, Key: key, Message: msg, CommentOut: commentOut})
	}

	for _, p := range cfg.Problems {
		add(Warn, p.Line, "", fmt.Sprintf("malformed line (%s); WSL skips it: %q", p.Msg, strings.TrimSpace(p.Raw)), true)
	}

	seen := map[string]int{}
	values := map[string]string{}
	for _, e := range cfg.Entries {
		id := strings.ToLower(e.Section + "." + e.Key)
		if prev, dup := seen[id]; dup {
			add(Info, e.Line, id, fmt.Sprintf("duplicate of line %d; the last value wins", prev), false)
		}
		seen[id] = e.Line
		values[id] = e.Value

		if e.Section == "" {
			add(Warn, e.Line, e.Key, "key before any [section] header; WSL ignores it (put it under [wsl2])", true)
			continue
		}
		k, ok := table.Lookup(e.Section, e.Key)
		if !ok {
			if secs := table.SectionsFor(e.Key); len(secs) > 0 {
				add(Warn, e.Line, id, fmt.Sprintf("%s is not a key of [%s]; WSL ignores it here. It belongs under [%s]", e.Key, e.Section, strings.Join(secs, "] or [")), true)
			} else {
				add(Warn, e.Line, id, "unknown key; WSL silently ignores it", true)
			}
			continue
		}
		if k.AliasOf != "" {
			add(Info, e.Line, id, fmt.Sprintf("still accepted, but the setting moved to [%s]", k.AliasOf), false)
		}
		if !k.Documented && k.AliasOf == "" {
			add(Info, e.Line, id, "undocumented key (present in the WSL source, absent from the docs); behaviour may change without notice", false)
		}
		if k.MinWindowsBuild > 0 && host.WindowsBuild > 0 && host.WindowsBuild < k.MinWindowsBuild {
			add(Warn, e.Line, id, fmt.Sprintf("needs Windows build %d or newer (this host is %d); ignored here", k.MinWindowsBuild, host.WindowsBuild), true)
		}
		if k.MinWSL != "" && !host.Runtime.IsZero() {
			if min := wslver.MustParse(k.MinWSL); host.Runtime.Less(min) {
				add(Warn, e.Line, id, fmt.Sprintf("needs WSL %s or newer (installed %s); ignored here", min, host.Runtime), true)
			}
		}
		lintValue(e, id, k, host, add)
	}

	// Cross-key rules.
	mode := strings.ToLower(firstOf(values, "wsl2.networkingmode", "experimental.networkingmode"))
	if mode == "bridged" {
		add(Warn, seen[lineKey(values, "wsl2.networkingmode", "experimental.networkingmode")], "wsl2.networkingMode", "bridged networking is deprecated since WSL 2.4.5 and will be removed; use mirrored", false)
	}
	for _, k := range []string{"wsl2.vmswitch", "wsl2.macaddress"} {
		if _, ok := values[k]; ok && mode != "bridged" {
			add(Warn, seen[k], k, "only used with networkingMode=bridged; ignored", true)
		}
	}
	if mode == "mirrored" {
		if _, ok := values["wsl2.localhostforwarding"]; ok {
			add(Info, seen["wsl2.localhostforwarding"], "wsl2.localhostForwarding", "ignored with networkingMode=mirrored (ports are shared with the host)", false)
		}
		if host.WindowsBuild > 0 && host.WindowsBuild < 22621 {
			add(Fail, seen[lineKey(values, "wsl2.networkingmode", "experimental.networkingmode")], "wsl2.networkingMode", fmt.Sprintf("mirrored networking needs Windows 11 22H2 (build 22621); this host is %d. WSL falls back to NAT", host.WindowsBuild), false)
		}
	}
	if mode != "mirrored" {
		for _, k := range []string{"experimental.ignoredports", "experimental.hostaddressloopback"} {
			if _, ok := values[k]; ok {
				add(Info, seen[k], k, "only applies with networkingMode=mirrored", false)
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].LineNo < out[j].LineNo })
	return out
}

func lintValue(e Entry, id string, k data.ConfigKey, host Host, add func(Severity, int, string, string, bool)) {
	switch {
	case k.Type == "bool":
		if _, ok := ParseBool(e.Value); !ok {
			add(Warn, e.Line, id, fmt.Sprintf("%q is not a boolean (true/false); WSL uses the default", e.Value), false)
		}
	case k.Type == "int":
		if _, err := strconv.ParseInt(strings.TrimSpace(e.Value), 10, 64); err != nil {
			add(Warn, e.Line, id, fmt.Sprintf("%q is not an integer; WSL uses the default", e.Value), false)
		}
	case k.Type == "size":
		n, ok := ParseSize(e.Value)
		if !ok {
			add(Warn, e.Line, id, fmt.Sprintf("%q is not a size (e.g. 4GB, 512MB); WSL uses the default", e.Value), false)
			return
		}
		if id == "wsl2.memory" && host.TotalRAM > 0 && n > host.TotalRAM {
			add(Warn, e.Line, id, fmt.Sprintf("%s exceeds the host's %s of RAM", e.Value, humanBytes(host.TotalRAM)), false)
		}
	case k.Type == "path":
		if host.PathExists != nil && e.Value != "" && !host.PathExists(unescape(e.Value)) {
			sev := Warn
			msg := "path does not exist"
			if id == "wsl2.kernel" {
				sev, msg = Fail, "custom kernel path does not exist; the VM cannot start (WSL_E_CUSTOM_KERNEL_NOT_FOUND)"
			}
			add(sev, e.Line, id, fmt.Sprintf("%s: %s", msg, e.Value), false)
		}
	case strings.HasPrefix(k.Type, "enum:"):
		allowed := k.EnumValues()
		found := false
		for _, a := range allowed {
			if strings.EqualFold(a, e.Value) {
				found = true
				break
			}
		}
		if !found {
			add(Warn, e.Line, id, fmt.Sprintf("%q is not one of %s; WSL falls back to the default", e.Value, strings.Join(allowed, ", ")), false)
		}
	}
}

// unescape turns the doubled backslashes .wslconfig requires into a real path.
func unescape(p string) string {
	return strings.ReplaceAll(p, `\\`, `\`)
}

func firstOf(values map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := values[k]; ok {
			return v
		}
	}
	return ""
}

func lineKey(values map[string]string, keys ...string) string {
	for _, k := range keys {
		if _, ok := values[k]; ok {
			return k
		}
	}
	return ""
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d B", b)
}

// CommentOut returns the file's lines with every finding marked CommentOut
// disabled: a "# wslkit:<message>" line is inserted before it and the line
// itself is prefixed with "# ". The caller joins with the file's newline style.
func CommentOut(cfg *Config, findings []Finding) []string {
	reasons := map[int]string{}
	for _, f := range findings {
		if f.CommentOut && f.LineNo >= 1 && f.LineNo <= len(cfg.Lines) {
			if _, dup := reasons[f.LineNo]; !dup {
				reasons[f.LineNo] = f.Message
			}
		}
	}
	out := make([]string, 0, len(cfg.Lines)+len(reasons))
	for i, l := range cfg.Lines {
		reason, ok := reasons[i+1]
		if !ok || strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
			continue
		}
		out = append(out, "# wslkit:"+reason, "# "+l)
	}
	return out
}
