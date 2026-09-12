package actions

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
	"github.com/wslkit/wslkit/internal/probe/wsl"
	"github.com/wslkit/wslkit/internal/wslconfig"
)

// WslConfig comments out .wslconfig lines that WSL ignores or misreads, after
// backing the file up. Rollback restores the backup.
type WslConfig struct{}

func (WslConfig) ID() string     { return "wslconfig" }
func (WslConfig) Title() string  { return "Comment out ignored or broken .wslconfig settings" }
func (WslConfig) Elevates() bool { return false }

func (w WslConfig) Plan(e *env.Env, o fix.Options) (fix.Plan, error) {
	p := fix.Plan{FixID: w.ID(), Title: w.Title(), CreatedAt: time.Now()}
	if !e.Config.WslConfig.OK() {
		return p, fmt.Errorf("no readable .wslconfig at %s", e.Config.WslConfigPath)
	}
	table, err := data.LoadConfigKeys()
	if err != nil {
		return p, err
	}
	cfg := wslconfig.Parse(e.Config.WslConfig.Value)
	findings := wslconfig.Lint(cfg, table, wsl.LintHost(e))
	var fixable []wslconfig.Finding
	for _, f := range findings {
		if f.CommentOut {
			fixable = append(fixable, f)
		}
	}
	if len(fixable) == 0 {
		p.Steps = []fix.Step{{Kind: "note", Description: "Nothing to change: no ignored or malformed lines in " + e.Config.WslConfigPath}}
		return p, nil
	}
	lines := wslconfig.CommentOut(cfg, fixable)
	nl := "\n"
	if strings.Contains(e.Config.WslConfig.Value, "\r\n") {
		nl = "\r\n"
	}
	content := strings.Join(lines, nl)
	if strings.HasSuffix(e.Config.WslConfig.Value, "\n") && !strings.HasSuffix(content, "\n") {
		content += nl
	}
	backup := filepath.Join(filepath.Dir(e.Config.WslConfigPath), fmt.Sprintf(".wslconfig.wslkit-%s.bak", p.CreatedAt.UTC().Format("20060102-150405")))

	for _, f := range fixable {
		p.Warnings = append(p.Warnings, fmt.Sprintf("line %d (%s): %s", f.LineNo, f.Key, f.Message))
	}
	p.Steps = []fix.Step{
		{Kind: "file_copy", Args: []string{e.Config.WslConfigPath, backup}, Description: "Back up .wslconfig to " + backup},
		{Kind: "file_write", Args: []string{e.Config.WslConfigPath, content}, Description: fmt.Sprintf("Comment out %d line(s) in %s with a '# wslkit:' reason", len(fixable), e.Config.WslConfigPath)},
		{Kind: "note", Description: "Run wsl --shutdown so the VM picks up the new configuration (wslkit doctor fix shutdown)"},
	}
	p.Rollback = []fix.Step{
		{Kind: "file_copy", Args: []string{backup, e.Config.WslConfigPath}, Description: "Restore .wslconfig from " + backup},
	}
	return p, nil
}
