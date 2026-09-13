package wsl

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslconfig"
	"github.com/wslkit/wslkit/internal/wslver"
)

// WSL006 lints /etc/wsl.conf inside each running distribution.
//
// This file is where a surprising share of confusing WSL behaviour comes from,
// and nothing on the Windows side surfaces it: a typo in a key name is silently
// ignored, so a setting someone believes is on has simply never applied.
//
// It can only be read for a distribution that is already running. Reaching into
// a stopped one over \\wsl.localhost starts it, which a read-only diagnosis
// must not do, so a stopped distribution is reported as skipped with the
// reason rather than passed over in silence.

type WslConf struct{}

func (WslConf) base() probe.Base {
	return probe.Base{
		PID:        "WSL006",
		PTitle:     "wsl.conf inside each running distribution",
		PMilestone: "M2",
		PNeeds:     []string{"WSL004"},
	}
}

func (p WslConf) ID() string        { return p.base().ID() }
func (p WslConf) Title() string     { return p.base().Title() }
func (p WslConf) Milestone() string { return p.base().Milestone() }
func (p WslConf) Needs() []string   { return p.base().Needs() }

func (p WslConf) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Distros.OK() && !e.Distros.Absent() {
		return b.Res(probe.Unknown, 0.1, "could not read the distribution inventory: "+e.Distros.Err)
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.OK, 0, "No distributions are registered, so there is no wsl.conf to check")
	}

	table, tableErr := data.LoadWslConfKeys()
	if tableErr != nil {
		return b.Res(probe.Unknown, 0.1, "the wsl.conf key table could not be loaded: "+tableErr.Error())
	}

	var findings []string
	var details []string
	var skipped []string
	checked := 0

	for _, d := range distros {
		if d.Version != 2 {
			continue
		}
		switch {
		case d.WslConf.Absent():
			// No file at all is normal and correct: every setting in it
			// has a default.
			checked++
			continue
		case !d.WslConf.OK():
			skipped = append(skipped, fmt.Sprintf("%s (%s)", d.Name, reasonFor(d.WslConf)))
			continue
		}
		checked++
		found, detail := lintWslConf(d.Name, d.WslConf.Value, table, e)
		findings = append(findings, found...)
		details = append(details, detail...)
	}

	if checked == 0 && len(skipped) > 0 {
		r := b.Res(probe.Skipped, 0, fmt.Sprintf("No running distribution to read wsl.conf from (%s)", strings.Join(skipped, "; ")))
		r.Detail = "This file can only be read from a distribution that is already running. Start one and run the check again, or use --allow-vm-wake."
		return r
	}

	if len(findings) == 0 {
		msg := fmt.Sprintf("wsl.conf is clean in %d distribution(s)", checked)
		if len(skipped) > 0 {
			msg += fmt.Sprintf("; %d not running", len(skipped))
		}
		r := b.Res(probe.OK, 0, msg)
		r.Detail = strings.Join(details, "\n")
		return r
	}

	r := b.Res(probe.Warn, 0.45, strings.Join(findings, "; "))
	r.Detail = strings.Join(details, "\n")
	r.FixHint = "edit /etc/wsl.conf inside the distribution, then run wsl --terminate <name> for it to take effect"
	return r
}

// reasonFor turns the recorded provenance into something a reader can act on.
func reasonFor(f env.Field[string]) string {
	switch f.ErrKind {
	case env.ErrVMWakeRefused:
		return "not running"
	case env.ErrTimeout:
		return "timed out"
	case env.ErrNeedsElevation:
		return "access denied"
	case env.ErrUnsupported:
		return "not applicable"
	default:
		return f.Err
	}
}

// lintWslConf checks one file, returning short findings and longer detail.
func lintWslConf(distro, text string, table *data.WslConfKeys, e *env.Env) (findings, details []string) {
	cfg := wslconfig.Parse(text)

	// Lines that are neither blank, comment, section nor key=value. WSL
	// ignores them, so a setting written in the wrong shape does nothing and
	// says nothing.
	for _, pr := range cfg.Problems {
		findings = append(findings, fmt.Sprintf("%s: wsl.conf line %d is not a setting", distro, pr.Line))
		details = append(details, fmt.Sprintf("%s:%d  %s  (%s)", distro, pr.Line, strings.TrimSpace(pr.Raw), pr.Msg))
	}

	seen := map[string]int{}
	for _, entry := range cfg.Entries {
		section := strings.ToLower(entry.Section)
		key := strings.ToLower(entry.Key)
		id := section + "." + key

		// A key written twice: WSL takes one of them, and which one is not
		// something to rely on.
		if first, dup := seen[id]; dup {
			findings = append(findings, fmt.Sprintf("%s: %s is set twice in wsl.conf", distro, id))
			details = append(details, fmt.Sprintf("%s:%d  %s is already set on line %d; only one of them applies", distro, entry.Line, id, first))
			continue
		}
		seen[id] = entry.Line

		k, known := table.Lookup(section, key)
		if !known {
			if !table.KnownSection(section) {
				findings = append(findings, fmt.Sprintf("%s: wsl.conf has no [%s] section", distro, section))
				details = append(details, fmt.Sprintf("%s:%d  [%s] is not a section WSL reads, so nothing under it applies. The sections are: %s",
					distro, entry.Line, section, strings.Join(table.Sections(), ", ")))
				continue
			}
			findings = append(findings, fmt.Sprintf("%s: wsl.conf does not have a %s setting", distro, id))
			details = append(details, fmt.Sprintf("%s:%d  %s is ignored. %s",
				distro, entry.Line, id, nearestHint(table, section, key)))
			continue
		}

		if f, detail := checkValue(distro, entry, k, e); f != "" {
			findings = append(findings, f)
			details = append(details, detail)
		}
	}
	return findings, details
}

// nearestHint suggests the key someone probably meant.
//
// A misspelled key is silently ignored by WSL, so the whole value of finding
// one is telling the reader what it should have said.
func nearestHint(table *data.WslConfKeys, section, key string) string {
	best, bestDist := "", 1<<30
	for _, k := range table.Keys {
		if !strings.EqualFold(k.Section, section) {
			continue
		}
		if d := editDistance(strings.ToLower(k.Key), key); d < bestDist {
			best, bestDist = k.Key, d
		}
	}
	// Only suggest something genuinely close; an unrelated name is worse
	// than no suggestion.
	if best != "" && bestDist <= 3 {
		return fmt.Sprintf("Did you mean %s.%s?", section, best)
	}
	if names := table.KeysIn(section); len(names) > 0 {
		return fmt.Sprintf("[%s] takes: %s", section, strings.Join(names, ", "))
	}
	return ""
}

// editDistance is the usual Levenshtein distance, for suggesting a near miss.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// checkValue validates one setting against the table.
//
// The value rules matter more here than in most configuration files, because
// WSL does not complain: a value it cannot parse is treated as absent, so the
// setting silently reverts to its default and the user is left believing it
// applied.
func checkValue(distro string, entry wslconfig.Entry, k data.WslConfKey, e *env.Env) (finding, detail string) {
	id := strings.ToLower(k.Section) + "." + strings.ToLower(k.Key)
	value := strings.TrimSpace(entry.Value)
	// Values may be quoted; WSL strips the quotes, so the lint compares what
	// it would actually see.
	unquoted := strings.Trim(value, `"'`)

	// A setting the running WSL is too old to read is accepted and ignored,
	// which looks exactly like it working.
	if k.MinWSL != "" {
		if have, ok := runtimeVersion(e); ok {
			need, err := wslver.Parse(k.MinWSL)
			if err == nil && have.Less(need) {
				return fmt.Sprintf("%s: %s needs WSL %s", distro, id, k.MinWSL),
					fmt.Sprintf("%s:%d  %s is ignored by WSL %s; it was added in %s. The setting is not applying.",
						distro, entry.Line, id, have, k.MinWSL)
			}
		}
	}

	switch {
	case k.Type == "bool":
		switch strings.ToLower(unquoted) {
		case "true", "false":
		default:
			return fmt.Sprintf("%s: %s is not true or false", distro, id),
				fmt.Sprintf("%s:%d  %s = %s. WSL reads only true or false here; anything else is treated as absent, so the default (%s) applies.",
					distro, entry.Line, id, value, orUnset(k.Default))
		}

	case strings.HasPrefix(k.Type, "enum:"):
		allowed := k.EnumValues()
		ok := false
		for _, a := range allowed {
			if strings.EqualFold(a, unquoted) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Sprintf("%s: %s is not one of %s", distro, id, strings.Join(allowed, ", ")),
				fmt.Sprintf("%s:%d  %s = %s. It takes one of: %s.",
					distro, entry.Line, id, value, strings.Join(allowed, ", "))
		}

	case k.Type == "int":
		if unquoted == "" || strings.IndexFunc(unquoted, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return fmt.Sprintf("%s: %s is not a whole number", distro, id),
				fmt.Sprintf("%s:%d  %s = %s, which is not a whole number, so it is ignored.",
					distro, entry.Line, id, value)
		}

	case k.Type == "path":
		if unquoted == "" {
			return fmt.Sprintf("%s: %s is empty", distro, id),
				fmt.Sprintf("%s:%d  %s has no value, so it is ignored.", distro, entry.Line, id)
		}
		if !strings.HasPrefix(unquoted, "/") {
			return fmt.Sprintf("%s: %s is not an absolute path", distro, id),
				fmt.Sprintf("%s:%d  %s = %s. It is read inside the distribution, so it has to start with /.",
					distro, entry.Line, id, value)
		}
	}

	if k.Note != "" {
		return "", fmt.Sprintf("%s:%d  %s = %s. %s", distro, entry.Line, id, value, k.Note)
	}
	return "", ""
}

// runtimeVersion is the WSL runtime this machine is running, when it could be
// established.
func runtimeVersion(e *env.Env) (wslver.Version, bool) {
	for _, f := range []env.Field[string]{e.Runtime.Version, e.Runtime.MSIVersion, e.Runtime.InboxWslVersion} {
		if !f.OK() || f.Value == "" {
			continue
		}
		if v, err := wslver.Parse(f.Value); err == nil && !v.IsZero() {
			return v, true
		}
	}
	return wslver.Version{}, false
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}
