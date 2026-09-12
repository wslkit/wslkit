package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wslkit/wslkit/internal/wslerr"
)

// ContextInfo is one member of WSL's Context enum, in bit order.
type ContextInfo struct {
	Name string `json:"name"`
	Bit  int    `json:"bit"`
}

// Segment is the hand-curated meaning of one context name.
type Segment struct {
	Desc   string   `json:"desc"`
	Probes []string `json:"probes"`
	Note   string   `json:"note,omitempty"`
}

type Cause struct {
	Summary string   `json:"summary"`
	Probes  []string `json:"probes,omitempty"`
	Refs    []string `json:"refs,omitempty"`
}

// CodeInfo is the hand-curated meaning of one error code or exit code.
type CodeInfo struct {
	HRESULT string   `json:"hresult,omitempty"`
	Desc    string   `json:"desc"`
	Probes  []string `json:"probes"`
	Causes  []Cause  `json:"causes,omitempty"`
	Refs    []string `json:"refs,omitempty"`
	Note    string   `json:"note,omitempty"`
}

type Errors struct {
	Schema       string              `json:"schema"`
	Updated      string              `json:"updated"`
	SourceCommit string              `json:"source_commit"`
	Contexts     []ContextInfo       `json:"contexts"`
	CodeNames    []string            `json:"code_names"`
	Segments     map[string]Segment  `json:"segments"`
	Codes        map[string]CodeInfo `json:"codes"`
	ExitCodes    map[string]CodeInfo `json:"exit_codes"`
}

var (
	errOnce  sync.Once
	errTable *Errors
	errLoad  error
)

// LoadErrors returns the embedded error dictionary, parsed once.
func LoadErrors() (*Errors, error) {
	errOnce.Do(func() {
		b, err := files.ReadFile("files/errors.json")
		if err != nil {
			errLoad = err
			return
		}
		var e Errors
		if err := json.Unmarshal(b, &e); err != nil {
			errLoad = fmt.Errorf("errors.json: %w", err)
			return
		}
		if err := e.Validate(); err != nil {
			errLoad = err
			return
		}
		errTable = &e
	})
	return errTable, errLoad
}

// Validate checks structure: contexts sorted by unique bit, segments only for
// known contexts, code entries with a description.
func (e *Errors) Validate() error {
	if e.Schema != "wslkit/errors/v1" {
		return fmt.Errorf("errors.json: unexpected schema %q", e.Schema)
	}
	seen := map[int]bool{}
	names := map[string]bool{}
	for i, c := range e.Contexts {
		if c.Name == "" || seen[c.Bit] {
			return fmt.Errorf("errors.json: contexts[%d] invalid or duplicate bit %d", i, c.Bit)
		}
		if i > 0 && c.Bit <= e.Contexts[i-1].Bit {
			return fmt.Errorf("errors.json: contexts must be sorted by bit (at %s)", c.Name)
		}
		seen[c.Bit] = true
		names[c.Name] = true
	}
	for name, s := range e.Segments {
		if len(e.Contexts) > 0 && !names[name] {
			return fmt.Errorf("errors.json: segment %q is not a known context", name)
		}
		if s.Desc == "" {
			return fmt.Errorf("errors.json: segment %q needs a desc", name)
		}
	}
	for name, c := range e.Codes {
		if c.Desc == "" {
			return fmt.Errorf("errors.json: code %q needs a desc", name)
		}
		if c.HRESULT != "" && !strings.HasPrefix(c.HRESULT, "0x") {
			return fmt.Errorf("errors.json: code %q hresult must be 0x-prefixed", name)
		}
	}
	return nil
}

// AllProbeIDs returns every probe ID referenced anywhere, for consistency tests.
func (e *Errors) AllProbeIDs() []string {
	set := map[string]bool{}
	add := func(ids []string) {
		for _, id := range ids {
			set[id] = true
		}
	}
	for _, s := range e.Segments {
		add(s.Probes)
	}
	for _, c := range e.Codes {
		add(c.Probes)
		for _, cause := range c.Causes {
			add(cause.Probes)
		}
	}
	for _, c := range e.ExitCodes {
		add(c.Probes)
		for _, cause := range c.Causes {
			add(cause.Probes)
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Step is one decoded path segment.
type Step struct {
	Name  string
	Desc  string
	Note  string
	Known bool // appears in the Context enum
}

// Explanation is what `wslkit doctor explain` renders.
type Explanation struct {
	Parsed     wslerr.Parsed
	Steps      []Step
	CodeName   string    // resolved name, e.g. "HCS_E_HYPERV_NOT_INSTALLED"
	Code       *CodeInfo // nil when unknown
	KnownToWSL bool      // the name is in g_commonErrors (WSL prints it by name)
	Probes     []string  // deduplicated: code probes, cause probes, then segments deepest-first
}

// Explain decodes a parsed error against the dictionary.
func (e *Errors) Explain(p wslerr.Parsed) Explanation {
	ex := Explanation{Parsed: p, CodeName: p.Code}
	known := map[string]bool{}
	for _, c := range e.Contexts {
		known[c.Name] = true
	}
	for _, seg := range p.Segments {
		st := Step{Name: seg, Known: known[seg]}
		if s, ok := e.Segments[seg]; ok {
			st.Desc, st.Note = s.Desc, s.Note
		}
		ex.Steps = append(ex.Steps, st)
	}

	// Resolve the code: exit code, by name, or by HRESULT.
	if p.ExitCode {
		if c, ok := e.ExitCodes[p.Code]; ok {
			cc := c
			ex.Code = &cc
		}
	} else if c, ok := e.Codes[p.Code]; ok {
		cc := c
		ex.Code = &cc
	} else if p.HRESULT != 0 {
		want := fmt.Sprintf("0x%08X", p.HRESULT)
		for name, c := range e.Codes {
			if strings.EqualFold(c.HRESULT, want) {
				cc := c
				ex.Code, ex.CodeName = &cc, name
				break
			}
		}
	}
	for _, n := range e.CodeNames {
		if n == ex.CodeName {
			ex.KnownToWSL = true
			break
		}
	}

	var probes []string
	seen := map[string]bool{}
	add := func(ids []string) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				probes = append(probes, id)
			}
		}
	}
	if ex.Code != nil {
		add(ex.Code.Probes)
		for _, c := range ex.Code.Causes {
			add(c.Probes)
		}
	}
	for i := len(p.Segments) - 1; i >= 0; i-- {
		if s, ok := e.Segments[p.Segments[i]]; ok {
			add(s.Probes)
		}
	}
	ex.Probes = probes
	return ex
}
