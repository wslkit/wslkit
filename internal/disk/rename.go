package disk

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// WSL keeps a distribution's name in one registry value, DistributionName on
// its Lxss key, and wsl.exe has no command that changes it (microsoft/WSL#4241).
// Measured on 3.0.1: the service reads the value live, so writing it renames
// the distribution at once, and the old name stops resolving.
//
// Everything that launches by GUID survives. The Windows Terminal profile and
// the Start-menu shortcut WSL writes for a modern distribution both run
// `wsl.exe --distribution-id {guid}`, so they keep working and only show the
// old name. Anything that says the name breaks: \\wsl.localhost\<old> paths and
// scripts running wsl -d <old>.

// validName is what wsl --import accepts. Measured on 3.0.1: letters, digits,
// '.', '-' and '_' pass; a space, ':' and a non-ASCII letter are refused with
// "Invalid distribution name".
var validName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// PlanRename describes renaming a distribution.
func PlanRename(e Env, r Registration, newName string, list []Registration, running, runningKnown bool) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	newName = strings.TrimSpace(newName)
	switch {
	case newName == "":
		return p, fmt.Errorf("%w: the new name is empty", ErrRefused)
	case !validName.MatchString(newName):
		return p, fmt.Errorf("%w: %q is not a name WSL accepts: use letters, digits, '.', '-' and '_' only", ErrRefused, newName)
	case newName == r.Name:
		return p, fmt.Errorf("%w: %s is already called that", ErrRefused, r.Name)
	}
	// Case-insensitively, because that is how wsl.exe matches a name. A
	// change of case alone is allowed: it is the same distribution.
	for _, other := range list {
		if other.GUID != r.GUID && strings.EqualFold(other.Name, newName) {
			return p, fmt.Errorf("%w: a distribution called %s already exists", ErrRefused, other.Name)
		}
	}
	if !runningKnown {
		return p, fmt.Errorf("%w: whether %s is running could not be determined, and renaming a running distribution leaves its sessions under a name that no longer resolves", ErrRefused, r.Name)
	}
	if running {
		return p, fmt.Errorf("%w: stop %s first with wsl --terminate %s", ErrRunning, r.Name, r.Name)
	}

	p.AddUndoable("rename %s to %s in its registration", r.Name, newName)
	p.Add("start %s to check the new name works", newName)
	p.Warn(fmt.Sprintf(`\\wsl.localhost\%s and \\wsl$\%s paths, and anything running wsl -d %s, stop working`, r.Name, r.Name, r.Name),
		fmt.Sprintf("use %s from now on; wslkit doctor undo puts the old name back", newName))
	if r.TerminalProfilePath != "" || r.ShortcutPath != "" {
		p.Warn("the Windows Terminal profile and the Start-menu shortcut keep working, since they launch it by GUID, but still show the old name",
			"rename them by hand if the old name bothers you")
	}
	return p, nil
}

// RenameResult is the outcome.
type RenameResult struct {
	GUID    string
	OldName string
	NewName string
}

// Rename writes the new name, then proves it by starting the distribution
// under it. If it does not start, the old name is put back.
func Rename(ctx context.Context, e Env, r Registration, newName string, pr Progress) (RenameResult, error) {
	if pr == nil {
		pr = DiscardProgress{}
	}
	newName = strings.TrimSpace(newName)
	res := RenameResult{GUID: r.GUID, OldName: r.Name, NewName: newName}

	pr.Step(fmt.Sprintf("rename %s to %s", r.Name, newName))
	if err := e.Registry.WriteString(r.GUID, "DistributionName", newName); err != nil {
		return res, err
	}

	pr.Step(fmt.Sprintf("start %s to check the new name works", newName))
	if err := start(ctx, e, newName); err != nil {
		if uerr := e.Registry.WriteString(r.GUID, "DistributionName", r.Name); uerr != nil {
			return res, fmt.Errorf("%w under the name %s: %v, and putting the old name back failed too: %v. Its registry key is %s", ErrSmokeTest, newName, err, uerr, r.GUID)
		}
		return res, fmt.Errorf("%w under the name %s: %v. The old name has been put back", ErrSmokeTest, newName, err)
	}
	return res, nil
}

// RenameJSON is the object printed for --json.
func RenameJSON(res RenameResult) map[string]any {
	return map[string]any{
		"guid":         res.GUID,
		"old_name":     res.OldName,
		"distribution": res.NewName,
		"renamed":      true,
	}
}
