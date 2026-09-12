// Package render turns an Env plus results into human text, JSON, or a
// redacted markdown report.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/redact"
	"github.com/wslkit/wslkit/internal/wslver"
)

const ResultSchema = "wslkit/result/v1"

type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Output is the --json envelope. It is also accepted by --from-snapshot.
type Output struct {
	Schema  string         `json:"schema"`
	Tool    Tool           `json:"tool"`
	Env     *env.Env       `json:"env"`
	Results []probe.Result `json:"results"`
}

type Options struct {
	Version string
	Width   int
	Redact  bool // apply redaction to human/json output too (report always redacts)
	Verbose bool // show OK/SKIPPED details
}

func rules(e *env.Env) redact.Rules {
	return redact.Rules{UserProfile: e.UserProfile, Hostname: e.Hostname}
}

// JSON writes the envelope. Redaction, if requested, is applied to the encoded bytes.
func JSON(w io.Writer, e *env.Env, results []probe.Result, o Options) error {
	out := Output{Schema: ResultSchema, Tool: Tool{"wslkit", o.Version}, Env: e, Results: results}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	s := string(b)
	if o.Redact {
		s = redact.String(s, rules(e))
	}
	_, err = io.WriteString(w, s+"\n")
	return err
}

// LoadSnapshot accepts either a bare Env or a full Output envelope.
func LoadSnapshot(data []byte) (*env.Env, error) {
	var probeKind struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &probeKind); err != nil {
		return nil, fmt.Errorf("snapshot is not JSON: %w", err)
	}
	switch {
	case strings.HasPrefix(probeKind.Schema, "wslkit/result/"), strings.HasPrefix(probeKind.Schema, "wsldoctor/result/"):
		var out Output
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		if out.Env == nil {
			return nil, fmt.Errorf("snapshot envelope has no env")
		}
		return out.Env, nil
	case strings.HasPrefix(probeKind.Schema, "wslkit/env/"), strings.HasPrefix(probeKind.Schema, "wsldoctor/env/"):
		var e env.Env
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return &e, nil
	default:
		return nil, fmt.Errorf("unknown snapshot schema %q", probeKind.Schema)
	}
}

// Human writes the ranked findings. No colour, no glyphs: this gets pasted into issues.
func Human(w io.Writer, e *env.Env, results []probe.Result, o Options) {
	var sb strings.Builder
	sb.WriteString(header(e, o.Version))
	sb.WriteString("\n\n")
	for _, r := range results {
		fmt.Fprintf(&sb, "%-8s%-8s%s\n", r.Status, r.ID, r.Summary)
		show := r.Status == probe.Fail || r.Status == probe.Warn || r.Status == probe.Unknown || o.Verbose
		if show && r.Detail != "" {
			for _, line := range strings.Split(r.Detail, "\n") {
				sb.WriteString("        " + line + "\n")
			}
		}
		if show && r.FixHint != "" {
			sb.WriteString("        -> " + r.FixHint + "\n")
		}
		if show && len(r.Refs) > 0 && o.Verbose {
			for _, ref := range r.Refs {
				sb.WriteString("        ref: " + ref + "\n")
			}
		}
		if show {
			sb.WriteString("\n")
		}
	}
	sb.WriteString(footer(e, results))
	s := sb.String()
	if o.Redact {
		s = redact.String(s, rules(e))
	}
	io.WriteString(w, s)
}

// Report writes a redacted markdown block for bug reports.
func Report(w io.Writer, e *env.Env, results []probe.Result, o Options) {
	var sb strings.Builder
	sb.WriteString("<details><summary>wslkit doctor report</summary>\n\n```\n")
	sb.WriteString(header(e, o.Version))
	sb.WriteString("\n\n")
	for _, r := range results {
		fmt.Fprintf(&sb, "%-8s%-8s%s\n", r.Status, r.ID, r.Summary)
		if r.Status != probe.OK && r.Detail != "" {
			for _, line := range strings.Split(r.Detail, "\n") {
				sb.WriteString("        " + line + "\n")
			}
		}
		if r.FixHint != "" && r.Status != probe.OK {
			sb.WriteString("        -> " + r.FixHint + "\n")
		}
	}
	sb.WriteString("```\n\n")
	sb.WriteString("Collected " + e.CollectedAt.UTC().Format("2006-01-02 15:04 UTC"))
	if !e.Elevated {
		sb.WriteString(", not elevated (UNKNOWN items may resolve with `wslkit doctor check --elevated`)")
	}
	sb.WriteString(".\n")
	sb.WriteString("Deeper traces: https://github.com/microsoft/WSL/blob/master/diagnostics/collect-wsl-logs.ps1\n")
	sb.WriteString("To turn this machine into a regression test, attach `wslkit doctor check --json > wslkit-env.json` to a wslkit/wslkit issue.\n")
	sb.WriteString("</details>\n")
	io.WriteString(w, redact.String(sb.String(), rules(e)))
}

func header(e *env.Env, version string) string {
	var parts []string
	parts = append(parts, "wslkit "+version)
	if e.Runtime.Version.OK() {
		v := e.Runtime.Version.Value
		if p, err := wslver.Parse(v); err == nil {
			v = p.String()
		}
		parts = append(parts, "WSL "+v)
	} else {
		parts = append(parts, "WSL not found")
	}
	if e.Host.OS.OK() {
		os := e.Host.OS.Value
		s := fmt.Sprintf("Windows %d.%d.%d", os.Major, os.Minor, os.Build)
		if os.DisplayVersion != "" {
			s += " (" + os.DisplayVersion + ")"
		}
		parts = append(parts, s)
	}
	n := len(e.DistroList())
	switch n {
	case 1:
		parts = append(parts, "1 distro")
	default:
		parts = append(parts, fmt.Sprintf("%d distros", n))
	}
	if !e.Elevated {
		parts = append(parts, "not elevated")
	}
	if e.Host.Arch != "" {
		parts = append(parts, e.Host.Arch)
	}
	return strings.Join(parts, "   ")
}

func footer(e *env.Env, results []probe.Result) string {
	var fail, warn, unknown, ok, skipped int
	for _, r := range results {
		switch r.Status {
		case probe.Fail:
			fail++
		case probe.Warn:
			warn++
		case probe.Unknown:
			unknown++
		case probe.OK:
			ok++
		case probe.Skipped:
			skipped++
		}
	}
	s := fmt.Sprintf("%d fail, %d warn, %d unknown, %d ok, %d skipped.", fail, warn, unknown, ok, skipped)
	if unknown > 0 && !e.Elevated {
		s += "  Re-run from an elevated terminal with --elevated to resolve UNKNOWN items."
	}
	return s + "\n"
}
