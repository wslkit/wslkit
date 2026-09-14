// Package wslconfig parses .wslconfig / wsl.conf INI text while keeping line
// numbers and raw text, so lint findings and the wslconfig fix can point at
// exact lines. Lint rules live in lint.go.
package wslconfig

import (
	"bufio"
	"strings"
)

type Entry struct {
	Section string
	Key     string
	Value   string
	Line    int    // 1-based
	Raw     string // original line
}

type Config struct {
	Entries  []Entry
	Sections []string       // in order of first appearance, lower-cased
	Lines    []string       // raw lines, for rewrites
	Problems []ParseProblem // lines that are neither blank, comment, section nor key=value
}

type ParseProblem struct {
	Line int
	Raw  string
	Msg  string
}

// Parse never fails; malformed lines are recorded in Problems.
func Parse(text string) *Config {
	c := &Config{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	section := ""
	seen := map[string]bool{}
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		c.Lines = append(c.Lines, raw)
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				c.Problems = append(c.Problems, ParseProblem{n, raw, "unterminated section header"})
				continue
			}
			section = strings.ToLower(strings.TrimSpace(line[1:end]))
			if !seen[section] {
				seen[section] = true
				c.Sections = append(c.Sections, section)
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			c.Problems = append(c.Problems, ParseProblem{n, raw, "expected key=value"})
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip trailing inline comment only when preceded by whitespace.
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		val = strings.Trim(val, `"`)
		c.Entries = append(c.Entries, Entry{Section: section, Key: key, Value: val, Line: n, Raw: raw})
	}
	return c
}

// Get returns the last value for section/key (case-insensitive), like WSL does.
func (c *Config) Get(section, key string) (string, bool) {
	var out string
	found := false
	for _, e := range c.Entries {
		if strings.EqualFold(e.Section, section) && strings.EqualFold(e.Key, key) {
			out, found = e.Value, true
		}
	}
	return out, found
}

// ParseSize parses WSL size values: bytes by default, or with KB/MB/GB/TB suffix.
func ParseSize(s string) (uint64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := uint64(1)
	for _, suf := range []struct {
		s string
		m uint64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, suf.s) {
			s = strings.TrimSuffix(s, suf.s)
			mult = suf.m
			break
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + uint64(ch-'0')
		if n > 1<<50 {
			return 0, false
		}
	}
	return n * mult, true
}

// ParseBool accepts true/false/1/0/yes/no, case-insensitively.
func ParseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	}
	return false, false
}

// NetworkingMode works out which networking mode WSL will use.
//
// The rule is WSL's own, and the order matters: a group policy value beats
// whatever .wslconfig says, and [experimental] is where the key lived before it
// was promoted to [wsl2], so a machine configured a year ago still has it
// there. Absent everything, NAT is the default.
//
// It lives here, in the parser, because both the doctor's networking check and
// the proxy command have to answer the same question and must not answer it
// differently.
func NetworkingMode(cfgText, policy string) (mode string, explicit bool, fromPolicy bool) {
	mode = "nat"
	if cfgText != "" {
		cfg := Parse(cfgText)
		if v, ok := cfg.Get("wsl2", "networkingMode"); ok {
			mode, explicit = strings.ToLower(strings.TrimSpace(v)), true
		} else if v, ok := cfg.Get("experimental", "networkingMode"); ok {
			mode, explicit = strings.ToLower(strings.TrimSpace(v)), true
		}
	}
	if p := strings.TrimSpace(policy); p != "" {
		return strings.ToLower(p), true, true
	}
	return mode, explicit, false
}
