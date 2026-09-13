package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/fix/actions"
	"github.com/wslkit/wslkit/internal/probe/all"
)

// generated builds the reference pages from the tables the binary embeds and
// from its own registries.
//
// The point of generating them is that they cannot drift. A hand-written list
// of probes is a list that is wrong the first time a probe is added and nobody
// notices for a release.
func generated(root string) (map[string]page, error) {
	out := map[string]page{}

	errs, err := data.LoadErrors()
	if err != nil {
		return nil, err
	}
	out["errors"] = errorsPage(errs)

	keys, err := data.LoadConfigKeys()
	if err != nil {
		return nil, err
	}
	out["wslconfig"] = wslconfigPage(keys)

	compat, err := data.LoadCompat()
	if err != nil {
		return nil, err
	}
	out["compatibility"] = compatPage(compat)

	out["probes"] = probesPage()
	out["fixes"] = fixesPage()

	decisions, err := decisionsPage(filepath.Join(root, "docs", "decisions"))
	if err != nil {
		return nil, err
	}
	out["decisions"] = decisions
	return out, nil
}

const generatedNote = "This page is generated from the same table the binary uses, so it cannot drift from what the tool actually does. Edit the data file, not this page."

func errorsPage(e *data.Errors) page {
	var b strings.Builder
	b.WriteString("WSL error codes, what they mean, and which checks explain them.\n\n")
	fmt.Fprintf(&b, "> %s The source is `internal/data/files/errors.json`, refreshed from the WSL source at `%s`.\n\n", generatedNote, e.SourceCommit)
	b.WriteString("`wslkit doctor explain` takes any of these, in any form WSL prints them:\n")
	b.WriteString("a full `Error code: Wsl/Service/E_UNEXPECTED`, a bare `0x80370102`,\n")
	b.WriteString("a name on its own, or the exit code `4294967295`.\n\n")

	b.WriteString("## Error codes\n\n")
	// The HRESULT gets its own column rather than a line break inside the
	// first one: the site renders markdown with raw HTML off, so a <br>
	// would be silently dropped and the number would vanish.
	b.WriteString("| Code | HRESULT | Means | Explained by |\n|---|---|---|---|\n")
	for _, name := range sortedKeys(e.Codes) {
		c := e.Codes[name]
		hresult := "-"
		if c.HRESULT != "" {
			hresult = "`" + c.HRESULT + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", name, hresult, cell(c.Desc), probeList(c.Probes))
	}

	b.WriteString("\n## What each code can be caused by\n\n")
	for _, name := range sortedKeys(e.Codes) {
		c := e.Codes[name]
		if len(c.Causes) == 0 && c.Note == "" {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", name)
		if c.Note != "" {
			fmt.Fprintf(&b, "%s\n\n", c.Note)
		}
		for _, cause := range c.Causes {
			fmt.Fprintf(&b, "- %s", cause.Summary)
			if len(cause.Probes) > 0 {
				fmt.Fprintf(&b, " Checked by %s.", probeList(cause.Probes))
			}
			for _, ref := range cause.Refs {
				fmt.Fprintf(&b, " [%s](%s)", refLabel(ref), ref)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Exit codes wsl.exe returns\n\n")
	b.WriteString("| Exit code | Means | Explained by |\n|---|---|---|\n")
	for _, name := range sortedKeys(e.ExitCodes) {
		c := e.ExitCodes[name]
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, cell(c.Desc), probeList(c.Probes))
	}

	b.WriteString("\n## Where an error came from\n\n")
	b.WriteString("The middle part of `Wsl/Service/E_UNEXPECTED` is the context: the part of\n")
	b.WriteString("WSL that failed. It narrows the search more than the code does.\n\n")
	b.WriteString("| Context | Means | Explained by |\n|---|---|---|\n")
	for _, name := range sortedKeys(e.Segments) {
		s := e.Segments[name]
		desc := s.Desc
		if s.Note != "" {
			desc += " " + s.Note
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, cell(desc), probeList(s.Probes))
	}

	return page{
		title:   "Error codes",
		summary: "Every WSL error code wslkit knows, what causes it, and which checks explain it.",
		body:    b.String(),
	}
}

func wslconfigPage(t *data.ConfigKeys) page {
	var b strings.Builder
	b.WriteString("Every key `.wslconfig` accepts, and the version each one needs.\n\n")
	fmt.Fprintf(&b, "> %s The source is `internal/data/files/wslconfig-keys.json`, refreshed from the WSL source at `%s`.\n\n", generatedNote, t.SourceCommit)
	b.WriteString("`.wslconfig` lives in your Windows home directory and configures the utility\n")
	b.WriteString("VM as a whole. Per-distribution settings go in `/etc/wsl.conf` inside the\n")
	b.WriteString("distribution instead.\n\n")
	b.WriteString("A key marked as not documented is accepted by WSL but absent from the\n")
	b.WriteString("published documentation, so it may change without notice.\n\n")

	sections := map[string][]data.ConfigKey{}
	for _, k := range t.Keys {
		sections[k.Section] = append(sections[k.Section], k)
	}
	for _, section := range sortedKeys(sections) {
		keys := sections[section]
		sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })
		fmt.Fprintf(&b, "## [%s]\n\n", section)
		b.WriteString("| Key | Type | Default | Needs | Notes |\n|---|---|---|---|---|\n")
		for _, k := range keys {
			typ := k.Type
			if vals := k.EnumValues(); len(vals) > 0 {
				typ = "one of " + "`" + strings.Join(vals, "`, `") + "`"
			} else if typ != "" {
				typ = "`" + typ + "`"
			}
			def := "-"
			if k.Default != "" {
				def = "`" + k.Default + "`"
			}
			var needs []string
			if k.MinWSL != "" {
				needs = append(needs, "WSL "+k.MinWSL)
			}
			if k.MinWindowsBuild != 0 {
				needs = append(needs, fmt.Sprintf("Windows build %d", k.MinWindowsBuild))
			}
			var notes []string
			if k.AliasOf != "" {
				notes = append(notes, "another name for `"+k.AliasOf+"`")
			}
			if k.DeprecatedIn != "" {
				notes = append(notes, "deprecated in "+k.DeprecatedIn)
			}
			if !k.Documented {
				notes = append(notes, "not documented by Microsoft")
			}
			if !k.InSource {
				notes = append(notes, "documented but not found in the source")
			}
			if k.Note != "" {
				notes = append(notes, k.Note)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n",
				k.Key, orDash(typ), def, orDash(strings.Join(needs, ", ")), cell(strings.Join(notes, "; ")))
		}
		b.WriteString("\n")
	}
	return page{
		title:   ".wslconfig keys",
		summary: "Every key .wslconfig accepts, its type and default, and the WSL or Windows version it needs.",
		body:    b.String(),
	}
}

func compatPage(c *data.Compat) page {
	var b strings.Builder
	b.WriteString("Which distributions need which version of WSL, and why.\n\n")
	fmt.Fprintf(&b, "> %s The source is `internal/data/files/compat.json`, last refreshed %s.\n\n", generatedNote, c.Updated)
	fmt.Fprintf(&b, "The latest stable WSL at that point was %s.\n\n", c.LatestStable)

	b.WriteString("## Capabilities\n\n")
	b.WriteString("A capability is something the WSL runtime gained at a version, and that a\n")
	b.WriteString("distribution may require in order to boot at all.\n\n")
	b.WriteString("| Capability | Since | What it is |\n|---|---|---|\n")
	for _, name := range sortedKeys(c.Capabilities) {
		cap := c.Capabilities[name]
		desc := cap.Title
		if cap.Description != "" {
			desc += ". " + cap.Description
		}
		fmt.Fprintf(&b, "| `%s` | WSL %s | %s |\n", name, cap.Since, cell(desc))
	}

	b.WriteString("\n## Distributions\n\n")
	b.WriteString("| Distribution | Format | Needs | Symptom if too old |\n|---|---|---|---|\n")
	for _, d := range c.Distros {
		var needs []string
		needs = append(needs, d.Requires...)
		if d.MinRuntime != "" {
			needs = append(needs, "WSL "+d.MinRuntime)
		}
		symptom := d.Symptom
		if d.Cause != "" {
			symptom += " " + d.Cause
		}
		fmt.Fprintf(&b, "| %s %s | `%s` | %s | %s |\n",
			d.Flavor, d.OsVersion, d.Format, orDash(strings.Join(needs, ", ")), cell(symptom))
	}
	return page{
		title:   "Distribution compatibility",
		summary: "Which distributions need which WSL version, and what happens when the runtime is too old.",
		body:    b.String(),
	}
}

func probesPage() page {
	var b strings.Builder
	b.WriteString("Every check `wslkit doctor` runs.\n\n")
	fmt.Fprintf(&b, "> %s The source is the probe registry in `internal/probe/all`.\n\n", generatedNote)
	b.WriteString("Checks are read-only and need no administrator. A check whose dependency\n")
	b.WriteString("failed is reported as skipped rather than guessed at, so one broken fact\n")
	b.WriteString("does not produce a page of misleading findings.\n\n")

	b.WriteString("| Check | What it looks at | Milestone | Depends on |\n|---|---|---|---|\n")
	for _, p := range all.Probes() {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n",
			p.ID(), cell(p.Title()), p.Milestone(), probeList(p.Needs()))
	}
	fmt.Fprintf(&b, "\n%d checks in total, run in the order above.\n", len(all.Probes()))
	return page{
		title:   "Checks",
		summary: "Every check wslkit doctor runs, what it looks at, and what it depends on.",
		body:    b.String(),
	}
}

func fixesPage() page {
	var b strings.Builder
	b.WriteString("Every change `wslkit doctor fix` can make.\n\n")
	fmt.Fprintf(&b, "> %s The source is the fix registry in `internal/fix/actions`.\n\n", generatedNote)
	b.WriteString("Nothing here runs on its own. A fix is named explicitly, prints its plan by\n")
	b.WriteString("default, changes nothing until `--apply`, and records a rollback that\n")
	b.WriteString("`wslkit doctor undo` can replay.\n\n")

	b.WriteString("| Fix | What it changes | Needs administrator |\n|---|---|---|\n")
	for _, f := range actions.All() {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", f.ID(), cell(f.Title()), yesNo(f.Elevates()))
	}
	b.WriteString("\nA fix that needs an administrator requires a console that already has one.\n")
	b.WriteString("wslkit never relaunches itself to get one; see the decision record on\n")
	b.WriteString("elevation for why.\n")
	return page{
		title:   "Fixes",
		summary: "Every change wslkit doctor fix can make, and which of them need an administrator.",
		body:    b.String(),
	}
}

// ---------------------------------------------------------------- helpers

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cell makes a string safe to put in a markdown table.
//
// A pipe would end the column and a newline would end the row. Angle brackets
// matter too: the data has prose like "wsl --terminate <name>", and the site
// renders markdown with raw HTML off, so goldmark drops that as an unknown tag
// and the placeholder silently disappears. Character references render as the
// literal characters and are not raw HTML.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "-"
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// probeList renders check identifiers as links into the checks page.
func probeList(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "`"+id+"`")
	}
	return strings.Join(out, ", ")
}

// refLabel turns a URL into something readable in prose.
func refLabel(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "https://github.com/microsoft/WSL/issues/"); ok {
		return "microsoft/WSL#" + rest
	}
	if rest, ok := strings.CutPrefix(ref, "https://github.com/"); ok {
		return rest
	}
	return "reference"
}

// decisionsPage indexes the decision records.
//
// Generated, so a new record appears the moment it is committed rather than
// when someone remembers to add a line.
func decisionsPage(dir string) (page, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return page{}, fmt.Errorf("reading %s: %w", dir, err)
	}
	type record struct{ file, title, status string }
	var records []record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return page{}, err
		}
		r := record{file: e.Name()}
		for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			if t, ok := strings.CutPrefix(line, "# "); ok && r.title == "" {
				r.title = strings.TrimSpace(t)
			}
			if s, ok := strings.CutPrefix(line, "Status: "); ok && r.status == "" {
				r.status = strings.TrimSpace(s)
			}
		}
		if r.title == "" {
			return page{}, fmt.Errorf("%s has no heading", e.Name())
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].file < records[j].file })

	var b strings.Builder
	b.WriteString("Why wslkit is built the way it is.\n\n")
	fmt.Fprintf(&b, "> %s The source is `docs/decisions/`.\n\n", generatedNote)
	b.WriteString("Each record states one decision, what it rules out, and what it costs.\n")
	b.WriteString("A decision that turned out to be wrong is superseded by a later record\n")
	b.WriteString("rather than edited, so the reasoning stays readable after the fact.\n\n")
	b.WriteString("| Decision | Status |\n|---|---|\n")
	for _, r := range records {
		title := strings.TrimPrefix(r.title, "ADR ")
		fmt.Fprintf(&b, "| [%s](https://github.com/wslkit/wslkit/blob/main/docs/decisions/%s) | %s |\n",
			cell(title), r.file, cell(r.status))
	}
	return page{
		title:   "Decisions",
		summary: "Why wslkit is built the way it is, one record per decision.",
		body:    b.String(),
	}, nil
}
