// Package actions holds the concrete fixes.
package actions

import (
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wsldoctor/internal/env"
	"github.com/wslkit/wsldoctor/internal/fix"
)

func All() []fix.Fix { return []fix.Fix{Update{}, Shutdown{}, WslConfig{}, Defender{}} }

func Lookup(id string) (fix.Fix, bool) {
	for _, f := range All() {
		if strings.EqualFold(f.ID(), id) {
			return f, true
		}
	}
	return nil, false
}

// ---- update ----

type Update struct{}

func (Update) ID() string     { return "update" }
func (Update) Title() string  { return "Update the WSL runtime (wsl --update)" }
func (Update) Elevates() bool { return false }

func (u Update) Plan(e *env.Env, o fix.Options) (fix.Plan, error) {
	p := fix.Plan{FixID: u.ID(), Title: u.Title(), CreatedAt: time.Now()}
	cur := "unknown"
	if e.Runtime.Version.OK() {
		cur = e.Runtime.Version.Value
	}
	args := []string{"wsl.exe", "--update"}
	for _, a := range o.Args {
		if a == "--pre-release" {
			args = append(args, "--pre-release")
		}
	}
	p.Steps = []fix.Step{
		{Kind: "exec", Args: args, Description: fmt.Sprintf("Update WSL from %s to the latest release via the Microsoft Store / MSI channel", cur)},
	}
	p.Rollback = []fix.Step{
		{Kind: "note", Description: fmt.Sprintf("Reinstall the previous runtime: download wsl.%s.x64.msi from https://github.com/microsoft/WSL/releases/tag/%s and run it", cur, trimTrailingZero(cur))},
	}
	p.Warnings = []string{"wsl --update shuts down running distributions."}
	return p, nil
}

func trimTrailingZero(v string) string {
	if strings.Count(v, ".") == 3 && strings.HasSuffix(v, ".0") {
		return strings.TrimSuffix(v, ".0")
	}
	return v
}

// ---- shutdown ----

type Shutdown struct{}

func (Shutdown) ID() string     { return "shutdown" }
func (Shutdown) Title() string  { return "Shut down the WSL VM (wsl --shutdown)" }
func (Shutdown) Elevates() bool { return false }

func (s Shutdown) Plan(e *env.Env, o fix.Options) (fix.Plan, error) {
	p := fix.Plan{FixID: s.ID(), Title: s.Title(), CreatedAt: time.Now()}
	p.Steps = []fix.Step{{Kind: "exec", Args: []string{"wsl.exe", "--shutdown"}, Description: "Terminate all distributions and the WSL 2 VM"}}
	var names []string
	for _, d := range e.DistroList() {
		names = append(names, d.Name)
	}
	restart := "wsl.exe -d <distro>"
	if len(names) > 0 {
		restart = "wsl.exe -d " + names[0]
	}
	p.Rollback = []fix.Step{{Kind: "note", Description: "Start a distribution again: " + restart}}
	p.Warnings = []string{"Kills running Linux processes and unsaved work inside every distro."}
	return p, nil
}
