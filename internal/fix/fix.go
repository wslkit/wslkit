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

	"github.com/wslkit/wslkit/internal/env"
)

// Step is one operation. Kinds:
//   - "exec": run Args as a process
//   - "note": print Description only (manual step)
//   - "file_copy": Args[0] source, Args[1] destination (overwrites)
//   - "file_write": Args[0] path, Args[1] full new content
//   - "wmi_method": Args[0] namespace, Args[1] class, Args[2] static method,
//     Args[3] JSON object of input parameters (string arrays allowed); see
//     WMIMethod / DecodeWMIMethod
//   - "registry_set" (root, key, name, type, value) and "registry_delete"
//     (root, key, name) are reserved for later fixes.
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

// WMICall is the decoded form of a "wmi_method" step.
type WMICall struct {
	Namespace string
	Class     string
	Method    string
	Params    map[string]interface{}
}

// WMIMethod builds a "wmi_method" step. Params values should be strings,
// numbers, bools or []string.
func WMIMethod(description, namespace, class, method string, params map[string]interface{}) Step {
	b, _ := json.Marshal(params)
	return Step{Kind: "wmi_method", Args: []string{namespace, class, method, string(b)}, Description: description}
}

// DecodeWMIMethod parses a "wmi_method" step back into a call. JSON arrays of
// strings are converted to []string so the WMI layer can build SAFEARRAYs.
func DecodeWMIMethod(s Step) (WMICall, error) {
	if s.Kind != "wmi_method" || len(s.Args) != 4 {
		return WMICall{}, fmt.Errorf("not a wmi_method step: %q", s.Kind)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(s.Args[3]), &raw); err != nil {
		return WMICall{}, fmt.Errorf("wmi_method params: %w", err)
	}
	params := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		if arr, ok := v.([]interface{}); ok {
			strs := make([]string, 0, len(arr))
			for _, x := range arr {
				strs = append(strs, fmt.Sprint(x))
			}
			params[k] = strs
			continue
		}
		params[k] = v
	}
	return WMICall{Namespace: s.Args[0], Class: s.Args[1], Method: s.Args[2], Params: params}, nil
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
// Rollback is not automatic: the journal entry holds it for `wslkit doctor undo`.
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
		switch s.Kind {
		case "exec":
			fmt.Fprintf(&sb, "     $ %s\n", strings.Join(s.Args, " "))
		case "wmi_method":
			if c, err := DecodeWMIMethod(s); err == nil {
				fmt.Fprintf(&sb, "     wmi %s : %s.%s\n", c.Namespace, c.Class, c.Method)
				keys := make([]string, 0, len(c.Params))
				for k := range c.Params {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&sb, "       %s = %v\n", k, c.Params[k])
				}
			}
		case "file_write":
			for _, l := range strings.Split(strings.TrimRight(s.Args[1], "\r\n"), "\n") {
				fmt.Fprintf(&sb, "     | %s\n", strings.TrimRight(l, "\r"))
			}
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

// DefaultJournalDir is %LOCALAPPDATA%\wslkit\undo (one journal for the whole
// kit), or a temp dir fallback.
func DefaultJournalDir() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "wslkit", "undo")
	}
	return filepath.Join(os.TempDir(), "wslkit", "undo")
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
