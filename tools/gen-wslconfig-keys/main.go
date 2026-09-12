// gen-wslconfig-keys refreshes the key list in internal/data/files/wslconfig-keys.json
// from a microsoft/WSL checkout (src/windows/common/WslCoreConfig.h). Curated
// fields (type, default, documented, gates, notes) are preserved per key; keys
// new to the source are appended with documented=false.
//
//	go run ./tools/gen-wslconfig-keys -wsl /path/to/WSL
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
	"strings"
	"time"
)

type key struct {
	Section         string `json:"section"`
	Key             string `json:"key"`
	Type            string `json:"type,omitempty"`
	Default         string `json:"default,omitempty"`
	Documented      bool   `json:"documented"`
	InSource        bool   `json:"in_source"`
	MinWindowsBuild int    `json:"min_windows_build,omitempty"`
	MinWSL          string `json:"min_wsl,omitempty"`
	DeprecatedIn    string `json:"deprecated_in,omitempty"`
	AliasOf         string `json:"alias_of,omitempty"`
	Note            string `json:"note,omitempty"`
}

type table struct {
	Schema       string `json:"schema"`
	Updated      string `json:"updated"`
	SourceCommit string `json:"source_commit"`
	Keys         []key  `json:"keys"`
}

var reKey = regexp.MustCompile(`static constexpr auto [A-Za-z0-9_]+ = "((wsl2|experimental|general)\.[A-Za-z0-9]+)";`)

func main() {
	wsl := flag.String("wsl", "", "path to a microsoft/WSL checkout")
	out := flag.String("out", "internal/data/files/wslconfig-keys.json", "table to update in place")
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
	src, err := os.ReadFile(filepath.Join(wsl, "src", "windows", "common", "WslCoreConfig.h"))
	if err != nil {
		return err
	}
	inSource := map[string]bool{}
	for _, m := range reKey.FindAllStringSubmatch(string(src), -1) {
		inSource[strings.ToLower(m[1])] = true
	}
	if len(inSource) < 20 {
		return fmt.Errorf("only %d keys found in WslCoreConfig.h; parser out of date", len(inSource))
	}

	var t table
	if b, err := os.ReadFile(out); err == nil {
		if err := json.Unmarshal(b, &t); err != nil {
			return fmt.Errorf("existing %s: %w", out, err)
		}
	}
	t.Schema = "wsldoctor/wslconfig-keys/v1"
	t.Updated = time.Now().UTC().Format("2006-01-02")
	t.SourceCommit = gitHead(wsl)

	seen := map[string]bool{}
	for i := range t.Keys {
		id := strings.ToLower(t.Keys[i].Section + "." + t.Keys[i].Key)
		t.Keys[i].InSource = inSource[id]
		seen[id] = true
	}
	for id := range inSource {
		if seen[id] {
			continue
		}
		parts := strings.SplitN(id, ".", 2)
		t.Keys = append(t.Keys, key{Section: parts[0], Key: parts[1], InSource: true, Note: "new in source; undocumented"})
		fmt.Fprintf(os.Stderr, "added new key %s\n", id)
	}
	sort.Slice(t.Keys, func(i, j int) bool {
		if t.Keys[i].Section != t.Keys[j].Section {
			return sectionOrder(t.Keys[i].Section) < sectionOrder(t.Keys[j].Section)
		}
		return strings.ToLower(t.Keys[i].Key) < strings.ToLower(t.Keys[j].Key)
	})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(t); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

func sectionOrder(s string) int {
	switch s {
	case "wsl2":
		return 0
	case "experimental":
		return 1
	default:
		return 2
	}
}

func gitHead(dir string) string {
	b, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}
