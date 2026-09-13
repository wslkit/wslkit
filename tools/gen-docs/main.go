// Command gen-docs builds the documentation site's content directory.
//
// It does two jobs. It stages the hand-written pages from docs/ into
// site/content/, adding the front matter Hugo needs and nothing else, so docs/
// stays plain markdown that renders on GitHub and reviews as a diff. And it
// generates the reference pages from the data the binary itself embeds and from
// its own registries, so the reference cannot drift from the tool: the error
// dictionary, the .wslconfig key table, the compatibility matrix, the probe
// catalogue and the fix catalogue are all written from the same values the
// commands use at runtime.
//
//	go run ./tools/gen-docs            stage and generate into site/content
//	go run ./tools/gen-docs -check     fail if anything would change
//
// site/content is generated and git-ignored. Never edit it; edit docs/, or the
// data file behind the page.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var (
		root   = flag.String("root", ".", "repository root")
		check  = flag.Bool("check", false, "fail if the generated pages are not what is on disk")
		outDir = flag.String("out", "", "output directory (default <root>/site/content)")
	)
	flag.Parse()

	if err := run(*root, *outDir, *check); err != nil {
		fmt.Fprintf(os.Stderr, "gen-docs: %v\n", err)
		os.Exit(1)
	}
}

func run(root, outDir string, check bool) error {
	docs := filepath.Join(root, "docs")
	if outDir == "" {
		outDir = filepath.Join(root, "site", "content")
	}

	nav, err := readNav(filepath.Join(root, "site", "data", "nav.yaml"))
	if err != nil {
		return err
	}

	pages, err := generated(root)
	if err != nil {
		return err
	}

	// Hand-written pages, staged as they are.
	staged, err := stage(docs, nav)
	if err != nil {
		return err
	}
	for slug, p := range staged {
		if _, clash := pages[slug]; clash {
			return fmt.Errorf("docs/%s.md and the generator both produce %q; rename one", slug, slug)
		}
		pages[slug] = p
	}

	// Every page must be in a section, and every section entry must exist.
	// One catches a page nothing links to, the other a nav entry pointing at
	// a page that was deleted or renamed.
	var missing, orphan []string
	for slug := range pages {
		if _, ok := nav.order[slug]; !ok {
			orphan = append(orphan, slug)
		}
	}
	for slug := range nav.order {
		if _, ok := pages[slug]; !ok {
			missing = append(missing, slug)
		}
	}
	sort.Strings(orphan)
	sort.Strings(missing)
	if len(orphan) > 0 {
		return fmt.Errorf("these pages are in no nav section, so nothing would link to them: %s", strings.Join(orphan, ", "))
	}
	if len(missing) > 0 {
		return fmt.Errorf("site/data/nav.yaml lists pages that do not exist: %s", strings.Join(missing, ", "))
	}

	home, err := os.ReadFile(filepath.Join(root, "site", "home.md"))
	if err != nil {
		return fmt.Errorf("reading site/home.md: %w", err)
	}
	pages["_index"] = page{body: string(home), title: "wslkit", isHome: true}

	return write(outDir, pages, nav, check)
}

// page is one rendered file.
type page struct {
	title   string
	summary string
	body    string
	// source is the repository path a reader can edit, empty for generated
	// pages, which have no single file to send them to.
	source string
	isHome bool
}

// navData is site/data/nav.yaml, reduced to what the generator needs.
type navData struct {
	order map[string]navEntry
}

type navEntry struct {
	section string
	weight  int
	short   string
}

// readNav parses the one shape nav.yaml uses.
//
// Taking a YAML dependency to read a file this project also writes, in a
// format this project chose, would be the more fragile choice rather than the
// less: ADR 0001 keeps the allow-list to two modules, and this is thirty lines.
func readNav(path string) (navData, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return navData{}, fmt.Errorf("reading %s: %w", path, err)
	}
	nav := navData{order: map[string]navEntry{}}
	section := ""
	inShort := false
	weight := 0

	for n, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		lineNo := n + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "short:"):
			inShort = true
			continue
		case strings.HasPrefix(line, "sections:"):
			inShort = false
			continue
		}
		if inShort {
			key, value, ok := strings.Cut(trimmed, ":")
			if !ok {
				return nav, fmt.Errorf("%s line %d: expected a short label", path, lineNo)
			}
			key = strings.TrimSpace(key)
			e, known := nav.order[key]
			if !known {
				return nav, fmt.Errorf("%s line %d: short label for %q, which is in no section", path, lineNo, key)
			}
			e.short = strings.Trim(strings.TrimSpace(value), `"'`)
			nav.order[key] = e
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "- section:"); ok {
			section = strings.Trim(strings.TrimSpace(rest), `"'`)
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "pages:"); ok {
			rest = strings.TrimSpace(rest)
			if !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
				return nav, fmt.Errorf("%s line %d: pages must be a [bracketed, list]", path, lineNo)
			}
			for _, slug := range strings.Split(rest[1:len(rest)-1], ",") {
				slug = strings.TrimSpace(slug)
				if slug == "" {
					continue
				}
				if _, dup := nav.order[slug]; dup {
					return nav, fmt.Errorf("%s line %d: %q is listed twice", path, lineNo, slug)
				}
				weight += 10
				nav.order[slug] = navEntry{section: section, weight: weight}
			}
			continue
		}
	}
	if len(nav.order) == 0 {
		return nav, fmt.Errorf("%s parsed to nothing; has its shape changed?", path)
	}
	return nav, nil
}

// stage reads the hand-written pages.
func stage(docs string, nav navData) (map[string]page, error) {
	entries, err := os.ReadDir(docs)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", docs, err)
	}
	out := map[string]page{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		// Pages that are not part of the site: the research notes and the
		// decision records have their own index pages, and the parity
		// checklist is a working document.
		if _, wanted := nav.order[slug]; !wanted {
			continue
		}
		b, err := os.ReadFile(filepath.Join(docs, e.Name()))
		if err != nil {
			return nil, err
		}
		title, summary, body := split(string(b))
		if title == "" {
			return nil, fmt.Errorf("docs/%s has no `# heading`, which is its title", e.Name())
		}
		out[slug] = page{
			title:   title,
			summary: summary,
			body:    body,
			source:  "docs/" + e.Name(),
		}
	}
	return out, nil
}

// split takes the title out of the first heading and a one-line summary out of
// the opening paragraph.
//
// First sentence only: anything longer is a paragraph pretending to be a
// summary, and it has to fit on a card.
func split(md string) (title, summary, body string) {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	start := 0
	for i, line := range lines {
		if t, ok := strings.CutPrefix(line, "# "); ok {
			title = strings.TrimSpace(t)
			start = i + 1
			break
		}
	}
	body = strings.TrimLeft(strings.Join(lines[start:], "\n"), "\n")

	var para []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		// Skip anything that is not prose: a badge row, a quote, a table.
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") ||
			strings.HasPrefix(t, "|") || strings.HasPrefix(t, "```") ||
			strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, t)
	}
	summary = firstSentence(strings.Join(para, " "))
	return title, summary, body
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// A full stop followed by a space, ignoring the ones inside `code`.
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			depth ^= 1
		case '.':
			if depth == 0 && i+1 < len(s) && s[i+1] == ' ' {
				return s[:i+1]
			}
		}
	}
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// write puts the staged pages into site/content.
func write(outDir string, pages map[string]page, nav navData, check bool) error {
	if !check {
		if err := os.RemoveAll(outDir); err != nil {
			return err
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
	}

	slugs := make([]string, 0, len(pages))
	for slug := range pages {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		p := pages[slug]
		var b bytes.Buffer
		b.WriteString("---\n")
		fmt.Fprintf(&b, "title: %s\n", quote(p.title))
		if p.summary != "" {
			fmt.Fprintf(&b, "summary: %s\n", quote(p.summary))
		}
		if e, ok := nav.order[slug]; ok {
			fmt.Fprintf(&b, "weight: %d\n", e.weight)
			if e.short != "" {
				fmt.Fprintf(&b, "short: %s\n", quote(e.short))
			}
		}
		if p.source != "" {
			fmt.Fprintf(&b, "source: %s\n", quote(p.source))
		}
		if p.isHome {
			b.WriteString("type: home\n")
		}
		b.WriteString("---\n\n")
		b.WriteString(strings.TrimRight(p.body, "\n"))
		b.WriteString("\n")

		path := filepath.Join(outDir, slug+".md")
		if check {
			have, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s is missing; run go run ./tools/gen-docs", path)
			}
			if !bytes.Equal(normalise(have), normalise(b.Bytes())) {
				return fmt.Errorf("%s is out of date; run go run ./tools/gen-docs", path)
			}
			continue
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func normalise(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }

// quote renders a YAML scalar safely. The titles and summaries here are prose
// that can contain a colon, which would otherwise end the key.
func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
