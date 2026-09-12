package actions

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
)

const (
	defenderNamespace = `root\Microsoft\Windows\Defender`
	defenderClass     = "MSFT_MpPreference"
)

// defenderProcesses are the WSL processes whose file I/O should not be scanned.
var defenderProcesses = []string{"vmmem", "vmmemWSL", "wslservice.exe"}

// Defender adds Windows Defender exclusions for every distro VHDX and the WSL
// VM processes through the MSFT_MpPreference WMI class (what Add-MpPreference
// wraps). Elevated only. Rollback removes exactly what was added.
type Defender struct{}

func (Defender) ID() string { return "defender" }
func (Defender) Title() string {
	return "Exclude WSL disks and processes from Defender real-time scanning"
}
func (Defender) Elevates() bool { return true }

func (d Defender) Plan(e *env.Env, o fix.Options) (fix.Plan, error) {
	p := fix.Plan{FixID: d.ID(), Title: d.Title(), CreatedAt: time.Now(), Elevates: true}
	if e.Defender.Present.OK() && !e.Defender.Present.Value || e.Defender.Present.Absent() {
		return p, fmt.Errorf("defender is not the active antivirus on this machine; nothing to exclude")
	}
	var paths []string
	for _, dist := range e.DistroList() {
		if dist.Version == 2 && dist.Vhd.OK() && dist.Vhd.Value.Path != "" {
			paths = append(paths, strings.TrimPrefix(dist.Vhd.Value.Path, `\\?\`))
		}
	}
	if len(paths) == 0 {
		return p, fmt.Errorf("no WSL 2 distribution disks found to exclude")
	}
	sort.Strings(paths)

	// Skip what is already excluded when the list was readable (elevated planning).
	var existingPaths, existingProcs map[string]bool
	if e.Defender.Exclusions.OK() {
		existingPaths = lowerSet(e.Defender.Exclusions.Value.Paths)
		existingProcs = lowerSet(e.Defender.Exclusions.Value.Processes)
	} else {
		p.Warnings = append(p.Warnings, "The current exclusion list could not be read; entries that already exist are re-added harmlessly.")
	}
	var addPaths, addProcs []string
	for _, pth := range paths {
		if !existingPaths[strings.ToLower(pth)] {
			addPaths = append(addPaths, pth)
		}
	}
	for _, proc := range defenderProcesses {
		if !existingProcs[strings.ToLower(proc)] {
			addProcs = append(addProcs, proc)
		}
	}
	if len(addPaths) == 0 && len(addProcs) == 0 {
		p.Steps = []fix.Step{{Kind: "note", Description: "Nothing to change: every WSL disk and process is already excluded."}}
		return p, nil
	}

	params := map[string]interface{}{}
	if len(addPaths) > 0 {
		params["ExclusionPath"] = addPaths
	}
	if len(addProcs) > 0 {
		params["ExclusionProcess"] = addProcs
	}
	desc := fmt.Sprintf("Add %d path exclusion(s) and %d process exclusion(s) via %s.Add", len(addPaths), len(addProcs), defenderClass)
	p.Steps = []fix.Step{
		fix.WMIMethod(desc, defenderNamespace, defenderClass, "Add", params),
		{Kind: "note", Description: "Verify with: wslkit doctor check --elevated   (DEF001 should report OK). On devices managed by Defender for Endpoint, tamper protection or policy can silently revert local exclusions; DEF001 will show that."},
	}
	p.Rollback = []fix.Step{
		fix.WMIMethod(fmt.Sprintf("Remove the same %d path and %d process exclusion(s) via %s.Remove", len(addPaths), len(addProcs), defenderClass), defenderNamespace, defenderClass, "Remove", params),
	}
	p.Warnings = append(p.Warnings,
		"Excluded files are not scanned by real-time protection. The list is limited to WSL's own disk images and VM processes.",
		"Paths to be excluded:\n    "+strings.Join(addPaths, "\n    "),
	)
	return p, nil
}

func lowerSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[strings.ToLower(strings.TrimSuffix(s, `\`))] = true
	}
	return m
}
