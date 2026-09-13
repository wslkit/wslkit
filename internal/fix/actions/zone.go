package actions

import (
	"fmt"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
	"github.com/wslkit/wslkit/internal/wslpath"
)

// Zone deletes the :Zone.Identifier files a saved download leaves inside a
// distribution.
//
// It deletes exactly the paths the scan recorded, one step each, rather than
// re-walking the tree at apply time and removing whatever matches then. A
// destructive step that decides for itself what to delete cannot be reviewed
// before it runs, and the whole point of printing the plan first is that the
// list can be read.
//
// There is no rollback. The file holds where a download came from and nothing
// else; once it is gone that record is gone, and no copy of it exists anywhere
// to restore from. The plan says so rather than pretending otherwise.
type Zone struct{}

func (Zone) ID() string     { return "zone" }
func (Zone) Title() string  { return "Delete Zone.Identifier files left by saved downloads" }
func (Zone) Elevates() bool { return false }

func (z Zone) Plan(e *env.Env, o fix.Options) (fix.Plan, error) {
	p := fix.Plan{FixID: z.ID(), Title: z.Title(), CreatedAt: time.Now()}
	want, prefix, err := zoneArgs(o.Args)
	if err != nil {
		return p, err
	}

	distros := e.DistroList()
	if len(distros) == 0 {
		return p, fmt.Errorf("no distributions are registered")
	}

	matched := false
	var skipped []string
	for _, d := range distros {
		if want != "" && !strings.EqualFold(d.Name, want) {
			continue
		}
		if d.Version != 2 {
			continue
		}
		matched = true
		if !d.ZoneFiles.OK() {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", d.Name, zoneSkipReason(d.ZoneFiles)))
			continue
		}
		s := d.ZoneFiles.Value
		for _, rel := range s.Paths {
			if prefix != "" && !strings.HasPrefix(rel, prefix) {
				continue
			}
			// The scan records the path as Linux sees it, because that is
			// where the user has to recognise it. Deleting it happens from
			// Windows, over the same path the scan used.
			p.Steps = append(p.Steps, fix.Step{
				Kind:        "file_delete",
				Args:        []string{wslpath.UNC(d.Name, rel)},
				Description: "Delete " + d.Name + ":" + rel,
			})
		}
		if n := s.Count - len(s.Paths); n > 0 && prefix == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"%s: the scan recorded %d of %d file(s); run the fix again to clear the rest",
				d.Name, len(s.Paths), s.Count))
		}
		if s.Truncated {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"%s: the scan stopped before it finished, so there may be more than it found", d.Name))
		}
	}

	if want != "" && !matched {
		return p, fmt.Errorf("no WSL 2 distribution named %q", want)
	}
	if len(p.Steps) == 0 {
		msg := "Nothing to delete: no Zone.Identifier files were found"
		if len(skipped) > 0 {
			msg += ". Not scanned: " + strings.Join(skipped, "; ") +
				". These files can only be found in a distribution that is already running."
		}
		p.Steps = []fix.Step{{Kind: "note", Description: msg}}
		return p, nil
	}

	p.Warnings = append(p.Warnings, fmt.Sprintf(
		"Deletes %d file(s). This cannot be undone.", len(p.Steps)))
	p.Rollback = []fix.Step{{Kind: "note", Description: "Not reversible. These files record where a download came from; deleting them loses that record, and the downloads themselves are untouched."}}
	return p, nil
}

// zoneArgs reads --distro and --path off the fix arguments.
//
// --path is a prefix filter against the path inside the distribution, so
// --path /home/ana narrows the fix to one user without having to name every
// file under it.
func zoneArgs(args []string) (distro, prefix string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--distro", "-d":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--distro needs a distribution name")
			}
			i++
			distro = args[i]
		case "--path":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--path needs a path inside the distribution")
			}
			i++
			prefix = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", "", fmt.Errorf("unknown option %q for fix zone", args[i])
			}
		}
	}
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		return "", "", fmt.Errorf("--path must be a path inside the distribution, starting with /")
	}
	return distro, prefix, nil
}

func zoneSkipReason(f env.Field[env.ZoneScan]) string {
	switch f.ErrKind {
	case env.ErrVMWakeRefused:
		return "not running"
	case env.ErrTimeout:
		return "timed out"
	case env.ErrNeedsElevation:
		return "access denied"
	case env.ErrUnsupported:
		return "not applicable"
	default:
		return f.Err
	}
}
