// Package fix defines remediation actions as plans executed through an
// Executor. Planning is pure; only executors touch the machine.
package fix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wslkit/wsldoctor/internal/env"
)

// Step is one operation. Kind "exec" runs Args as a process; "note" prints
// Description only (for manual steps); "registry_set" writes a value
// (Args: root, key, name, type, value); "registry_delete" (root, key, name).
type Step struct {
	Kind        string   `json:"kind"`
	Args        []string `json:"args,omitempty"`
	Description string   `json:"description"`
}

type Plan struct {
	FixID     string    `json:"fix_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Elevates  bool      `json:"elevates"`
	Steps     []Step    `json:"steps"`
	Rollback  []Step    `json:"rollback"`
	Warnings  []string  `json:"warnings,omitempty"`
}

type Options struct {
	Args []string // remaining CLI args after the fix id, e.g. --distro Ubuntu
}

type Fix interface {
	ID() string
	Title() string
	Elevates() bool
	Plan(e *env.Env, o Options) (Plan, error)
}

// Executor performs steps. RealExecutor lives in package fix/exec so this
// package stays free of os/exec.
type Executor interface {
	Run(s Step) error
}

// Recording captures steps without running them. Used by dry-run and tests.
type Recording struct{ Steps []Step }

func (r *Recording) Run(s Step) error { r.Steps = append(r.Steps, s); return nil }

// Apply runs every step; on the first error it returns which step failed.
// Rollback is not automatic: the journal entry holds it for `wsldoctor undo`.
func Apply(p Plan, ex Executor) error {
	for i, s := range p.Steps {
		if err := ex.Run(s); err != nil {
			return fmt.Errorf("step %d (%s) failed: %w", i+1, s.Description, err)
		}
	}
	return nil
}

// Describe renders a plan for humans.
func Describe(p Plan) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Fix %s: %s\n", p.FixID, p.Title)
	if p.Elevates {
		sb.WriteString("Requires an elevated terminal.\n")
	}
	for _, w := range p.Warnings {
		sb.WriteString("WARNING: " + w + "\n")
	}
	sb.WriteString("\nSteps:\n")
	for i, s := range p.Steps {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, s.Description)
		if s.Kind == "exec" {
			fmt.Fprintf(&sb, "     $ %s\n", strings.Join(s.Args, " "))
		}
	}
	sb.WriteString("\nRollback:\n")
	if len(p.Rollback) == 0 {
		sb.WriteString("  (none needed)\n")
	}
	for i, s := range p.Rollback {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, s.Description)
		if s.Kind == "exec" {
			fmt.Fprintf(&sb, "     $ %s\n", strings.Join(s.Args, " "))
		}
	}
	return sb.String()
}

// ---- Journal ----

// Journal persists applied plans so `undo` can replay Rollback.
type Journal struct{ Dir string }

// DefaultJournalDir is %LOCALAPPDATA%\wsldoctor\undo, or a temp dir fallback.
func DefaultJournalDir() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "wsldoctor", "undo")
	}
	return filepath.Join(os.TempDir(), "wsldoctor", "undo")
}

func (j Journal) Save(p Plan) (string, error) {
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s-%s", p.CreatedAt.UTC().Format("20060102-150405"), p.FixID)
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return id, os.WriteFile(filepath.Join(j.Dir, id+".json"), b, 0o600)
}

func (j Journal) Load(id string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(filepath.Join(j.Dir, filepath.Base(id)+".json"))
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

func (j Journal) List() ([]string, error) {
	entries, err := os.ReadDir(j.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}
