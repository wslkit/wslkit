package disk

import (
	"fmt"
	"io"
	"strings"
)

// DefaultScanPatterns are the directories WSL and the tools around it put disks
// in. Each is expanded against the environment and may contain a single `*`
// component.
var DefaultScanPatterns = []string{
	`%LOCALAPPDATA%\wsl\*`,                 // what wsl --install writes today
	`%LOCALAPPDATA%\Packages\*\LocalState`, // the older packaged layout
	`%LOCALAPPDATA%\Docker\wsl\*`,          // Docker Desktop
}

// Orphan is a virtual disk no registration claims.
type Orphan struct {
	Path string
	// SizeOnDisk is absent when it could not be measured. A file that went
	// away between the listing and the query is still worth reporting,
	// without a size.
	SizeOnDisk *uint64
}

// ScanOrphans finds .vhdx files that no distribution claims.
//
// Unclaimed is not the same as unused. Docker Desktop keeps a disk holding
// every volume the user has, and no registration points at it; deleting it
// because nothing claimed it would destroy their data. The report says so and
// the delete path checks each file is not open first.
func ScanOrphans(e Env, registrations []Registration, extra []string) ([]Orphan, []string, error) {
	claimed := map[string]bool{}
	for _, r := range registrations {
		if p := r.VhdPath(); p != "" {
			claimed[CanonicalPath(p)] = true
		}
	}

	patterns := append(append([]string{}, DefaultScanPatterns...), extra...)
	var warnings []string
	var out []Orphan
	seen := map[string]bool{}

	for _, pattern := range patterns {
		expanded, err := e.FS.ExpandEnv(pattern)
		if err != nil || expanded == "" {
			warnings = append(warnings, fmt.Sprintf("the scan root %s could not be expanded and was skipped", pattern))
			continue
		}
		dirs, warn := expandStar(e, expanded)
		warnings = append(warnings, warn...)
		for _, dir := range dirs {
			entries, err := e.FS.List(dir, "*.vhdx")
			if err != nil {
				// One unreadable directory must not stop the scan.
				warnings = append(warnings, fmt.Sprintf("%s could not be listed and was skipped: %v", dir, err))
				continue
			}
			for _, entry := range entries {
				if !isUnclaimedDisk(entry, claimed, seen) {
					continue
				}
				seen[CanonicalPath(entry.Path)] = true
				o := Orphan{Path: entry.Path}
				if n, err := e.FS.SizeOnDisk(entry.Path); err == nil {
					o.SizeOnDisk = &n
				}
				out = append(out, o)
			}
		}
	}
	return out, warnings, nil
}

// isUnclaimedDisk decides whether one directory entry is an orphan.
func isUnclaimedDisk(entry DirEntry, claimed, seen map[string]bool) bool {
	if entry.IsDir {
		return false
	}
	// The extension is re-checked here rather than trusted from the listing
	// pattern: the filesystem matches *.vhdx against the 8.3 short name too,
	// so disk.vhdx.bak comes back from a search for *.vhdx.
	if !strings.HasSuffix(strings.ToLower(entry.Path), ".vhdx") {
		return false
	}
	c := CanonicalPath(entry.Path)
	return !claimed[c] && !seen[c]
}

// expandStar resolves a pattern containing at most one `*` path component.
func expandStar(e Env, pattern string) ([]string, []string) {
	idx := strings.Index(pattern, "*")
	if idx < 0 {
		return []string{pattern}, nil
	}
	// Split into the parent, the wildcard component, and whatever follows.
	before := pattern[:idx]
	after := pattern[idx+1:]
	if strings.Contains(after, "*") {
		return nil, []string{fmt.Sprintf("the scan root %s has more than one wildcard, which is not supported", pattern)}
	}
	parent := strings.TrimRight(before, `\/`)
	tail := strings.TrimLeft(after, `\/`)

	entries, err := e.FS.List(parent, "*")
	if err != nil {
		return nil, []string{fmt.Sprintf("%s could not be listed and was skipped: %v", parent, err)}
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir {
			continue
		}
		if tail == "" {
			out = append(out, entry.Path)
			continue
		}
		out = append(out, joinWindows(entry.Path, tail))
	}
	return out, nil
}

// TotalSize sums what could be measured.
func TotalSize(orphans []Orphan) uint64 {
	var total uint64
	for _, o := range orphans {
		if o.SizeOnDisk != nil {
			total += *o.SizeOnDisk
		}
	}
	return total
}

// UnclaimedIsNotUnused is printed before any deletion.
const UnclaimedIsNotUnused = "not every disk here is unused: software other than WSL keeps virtual disks that no distribution claims. Check what each one is before deleting it."

// RenderOrphans writes the human-readable listing.
func RenderOrphans(w io.Writer, orphans []Orphan) {
	if len(orphans) == 0 {
		fmt.Fprintln(w, "no orphaned disks found")
		return
	}
	t := Table{Headers: []string{"SIZE ON DISK", "PATH"}}
	for _, o := range orphans {
		t.Rows = append(t.Rows, []string{sizeCell(o.SizeOnDisk), o.Path})
	}
	fmt.Fprint(w, t.String())
	fmt.Fprintf(w, "\n%s in %d file(s) that no distribution claims\n", FormatSize(TotalSize(orphans)), len(orphans))
}

// OrphanJSON is the object printed per orphan.
func OrphanJSON(o Orphan) map[string]any {
	m := map[string]any{"path": o.Path}
	putU64(m, "size_on_disk", o.SizeOnDisk)
	return m
}

// DeleteResult is the outcome for one file.
type DeleteResult struct {
	Path    string
	Deleted bool
	Err     error
}

// DeleteJSON is the object printed per deleted file.
func DeleteJSON(r DeleteResult) map[string]any {
	m := map[string]any{"path": r.Path, "deleted": r.Deleted}
	if r.Err != nil {
		m["error"] = r.Err.Error()
	}
	return m
}

// DeleteOrphans removes the named files, refusing any that are open.
//
// A failure does not stop the loop: the user asked for several files to go, and
// one being held is not a reason to leave the rest.
func DeleteOrphans(e Env, orphans []Orphan, remove func(path string) error) []DeleteResult {
	out := make([]DeleteResult, 0, len(orphans))
	for _, o := range orphans {
		res := DeleteResult{Path: o.Path}
		locked, err := e.FS.Locked(o.Path)
		switch {
		case err != nil:
			// The question could not be answered, which is not the
			// same as a no. Refuse rather than delete on a guess.
			res.Err = fmt.Errorf("whether %s is in use could not be determined, so it was left alone: %w", o.Path, err)
		case locked:
			res.Err = fmt.Errorf("%s is in use by another process", o.Path)
		default:
			if err := remove(o.Path); err != nil {
				res.Err = err
			} else {
				res.Deleted = true
			}
		}
		out = append(out, res)
	}
	return out
}
