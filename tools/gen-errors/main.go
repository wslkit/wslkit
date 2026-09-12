// gen-errors regenerates the machine-derived parts of internal/data/files/errors.json
// from a microsoft/WSL source checkout: the Context enum (bit order) and the
// names in g_commonErrors. Hand-curated parts (segments, codes, exit_codes) are
// preserved from the existing file.
//
//	go run ./tools/gen-errors -wsl /path/to/WSL -out internal/data/files/errors.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type contextInfo struct {
	Name string `json:"name"`
	Bit  int    `json:"bit"`
}

func main() {
	wsl := flag.String("wsl", "", "path to a microsoft/WSL checkout")
	out := flag.String("out", "internal/data/files/errors.json", "errors.json to update in place")
	flag.Parse()
	if *wsl == "" {
		fmt.Fprintln(os.Stderr, "-wsl is required")
		os.Exit(2)
	}
	if err := run(*wsl, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(wsl, out string) error {
	contexts, err := parseContexts(filepath.Join(wsl, "src", "windows", "common", "ExecutionContext.h"))
	if err != nil {
		return err
	}
	names, err := parseCommonErrors(filepath.Join(wsl, "src", "windows", "common", "wslutil.cpp"))
	if err != nil {
		return err
	}
	strs, err := parseContextStrings(filepath.Join(wsl, "src", "windows", "common", "wslutil.cpp"))
	if err != nil {
		return err
	}
	// Every enum member should have a display string; warn on drift.
	for _, c := range contexts {
		if !strs[c.Name] && c.Name != "Empty" {
			fmt.Fprintf(os.Stderr, "warning: context %s has no g_contextStrings entry\n", c.Name)
		}
	}

	doc := map[string]json.RawMessage{}
	if b, err := os.ReadFile(out); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("existing %s: %w", out, err)
		}
	}
	set := func(k string, v interface{}) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		doc[k] = b
		return nil
	}
	if err := set("schema", "wsldoctor/errors/v1"); err != nil {
		return err
	}
	if err := set("updated", time.Now().UTC().Format("2006-01-02")); err != nil {
		return err
	}
	if err := set("source_commit", gitHead(wsl)); err != nil {
		return err
	}
	if err := set("contexts", contexts); err != nil {
		return err
	}
	if err := set("code_names", names); err != nil {
		return err
	}
	for _, k := range []string{"segments", "codes", "exit_codes"} {
		if _, ok := doc[k]; !ok {
			if err := set(k, map[string]interface{}{}); err != nil {
				return err
			}
		}
	}
	// Stable key order for readable diffs.
	order := []string{"schema", "updated", "source_commit", "contexts", "code_names", "segments", "codes", "exit_codes"}
	var sb strings.Builder
	sb.WriteString("{\n")
	for i, k := range order {
		v, ok := doc[k]
		if !ok {
			continue
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, v, "  ", "  "); err != nil {
			return err
		}
		fmt.Fprintf(&sb, "  %q: %s", k, pretty.String())
		if i < len(order)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("}\n")
	return os.WriteFile(out, []byte(sb.String()), 0o644)
}

var (
	// `enum Context : ULONGLONG` today; tolerate `enum class Context` too.
	reEnumDecl   = regexp.MustCompile(`^\s*enum(\s+class)?\s+Context\b`)
	reEnumMember = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9]*)\s*=\s*0x([0-9A-Fa-f]+)\s*,?`)
)

func parseContexts(path string) ([]contextInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	in := false
	var out []contextInfo
	for _, l := range lines {
		if reEnumDecl.MatchString(l) {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), "}") {
			break
		}
		m := reEnumMember.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[2], 16, 64)
		if err != nil || v == 0 {
			continue
		}
		bit := 0
		for v > 1 {
			v >>= 1
			bit++
		}
		out = append(out, contextInfo{Name: m[1], Bit: bit})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Context enum members found in %s", path)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bit < out[j].Bit })
	return out, nil
}

var reX = regexp.MustCompile(`^\s*X(?:_WIN32)?\(([A-Za-z0-9_]+)\)`)

func parseTable(path, marker string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	in := false
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, marker) {
			in = true
		}
		if !in {
			continue
		}
		if m := reX.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
		if strings.Contains(l, "};") {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no entries found for %s in %s", marker, path)
	}
	return out, nil
}

func parseCommonErrors(path string) ([]string, error) {
	names, err := parseTable(path, "g_commonErrors{")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

func parseContextStrings(path string) (map[string]bool, error) {
	names, err := parseTable(path, "g_contextStrings{")
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m, nil
}

func gitHead(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
	b, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}
