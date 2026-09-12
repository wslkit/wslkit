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

// ---------------------------------------------------------------- WSL005

// ConfigLint lints %USERPROFILE%\.wslconfig against the key table derived from
// the WSL source plus the host's Windows build, runtime version and RAM.
type ConfigLint struct{}

func (ConfigLint) base() probe.Base {
	return probe.Base{PID: "WSL005", PTitle: ".wslconfig lint", PMilestone: "M2", PNeeds: []string{"WSL002"}}
}
func (p ConfigLint) ID() string        { return p.base().ID() }
func (p ConfigLint) Title() string     { return p.base().Title() }
func (p ConfigLint) Milestone() string { return p.base().Milestone() }
func (p ConfigLint) Needs() []string   { return p.base().Needs() }

// LintHost builds the linter's host facts from an Env; shared with fix wslconfig.
func LintHost(e *env.Env) wslconfig.Host {
	h := wslconfig.Host{}
	if e.Host.OS.OK() {
		h.WindowsBuild = e.Host.OS.Value.Build
	}
	if e.Runtime.Version.OK() {
		if v, err := wslver.Parse(e.Runtime.Version.Value); err == nil {
			h.Runtime = v
		}
	}
	if e.Host.TotalMemoryBytes.OK() {
		h.TotalRAM = e.Host.TotalMemoryBytes.Value
	}
	if e.Config.PathsExist != nil {
		exists := e.Config.PathsExist
		h.PathExists = func(p string) bool {
			v, ok := exists[strings.ToLower(p)]
			return !ok || v // unknown paths are not reported as missing
		}
	}
	return h
}

func (p ConfigLint) Run(e *env.Env) probe.Result {
	b := p.base()
	cfgField := e.Config.WslConfig
	switch {
	case cfgField.Absent():
		return b.Res(probe.OK, 0.3, "No .wslconfig; WSL uses defaults")
	case !cfgField.OK():
		return b.Res(probe.Unknown, 0.1, "could not read .wslconfig: "+cfgField.Err)
	}
	table, err := data.LoadConfigKeys()
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "key table failed to load: "+err.Error())
	}
	cfg := wslconfig.Parse(cfgField.Value)
	findings := wslconfig.Lint(cfg, table, LintHost(e))

	var fails, warns, infos []string
	for _, f := range findings {
		line := fmt.Sprintf("line %d", f.LineNo)
		if f.LineNo == 0 {
			line = "file"
		}
		msg := fmt.Sprintf("%s: %s", line, f.Message)
		if f.Key != "" {
			msg = fmt.Sprintf("%s: %s: %s", line, f.Key, f.Message)
		}
		switch f.Severity {
		case wslconfig.Fail:
			fails = append(fails, msg)
		case wslconfig.Warn:
			warns = append(warns, msg)
		default:
			infos = append(infos, msg)
		}
	}
	detail := strings.Join(append(append(fails, warns...), infos...), "\n")
	switch {
	case len(fails) > 0:
		r := b.Res(probe.Fail, 0.7, fmt.Sprintf(".wslconfig has %d setting(s) that stop WSL from starting", len(fails)))
		r.Detail = detail
		r.FixID, r.FixHint = "wslconfig", "edit "+e.Config.WslConfigPath+"   or: wslkit doctor fix wslconfig"
		return r
	case len(warns) > 0:
		r := b.Res(probe.Warn, 0.4, fmt.Sprintf(".wslconfig has %d setting(s) WSL ignores or misreads", len(warns)))
		r.Detail = detail
		r.FixID, r.FixHint = "wslconfig", "wslkit doctor fix wslconfig      (comments out ignored lines, with a backup)"
		return r
	default:
		r := b.Res(probe.OK, 0.4, fmt.Sprintf(".wslconfig is valid (%d settings)", len(cfg.Entries)))
		r.Detail = detail
		return r
	}
}
