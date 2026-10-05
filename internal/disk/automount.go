package disk

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// `wsl --mount <disk> --vhd` attaches an extra disk to the utility VM, and
// forgets it on every shutdown, reboot and idle timeout. WSL has no setting
// that re-attaches one (microsoft/WSL#11187, #8010), so people re-type the
// command or write their own scheduled task. This keeps a table of disks to
// attach and applies it (#88).
//
// Attach only: nothing here formats, partitions or writes to a disk.

// AutomountEntry is one disk to attach.
type AutomountEntry struct {
	// Path is the .vhdx or .vhd on Windows.
	Path string
	// Name mounts it at /mnt/wsl/<Name> instead of WSL's default name.
	Name string
	// Bare attaches the disk without mounting it, for a disk the guest
	// should see as a block device only.
	Bare bool
	// Type is the filesystem, ext4 when empty.
	Type string
	// Options are mount options.
	Options string
	// Partition is which partition to mount; zero means the whole disk.
	Partition int
}

// MountArgs is the wsl.exe command line that attaches the entry.
func (a AutomountEntry) MountArgs() []string {
	args := []string{"--mount", a.Path, "--vhd"}
	if a.Bare {
		return append(args, "--bare")
	}
	if a.Name != "" {
		args = append(args, "--name", a.Name)
	}
	if a.Type != "" {
		args = append(args, "--type", a.Type)
	}
	if a.Options != "" {
		args = append(args, "--options", a.Options)
	}
	if a.Partition > 0 {
		args = append(args, "--partition", strconv.Itoa(a.Partition))
	}
	return args
}

// ValidateAutomount checks an entry before it is added to the table.
func ValidateAutomount(a AutomountEntry, table []AutomountEntry, registered []Registration) error {
	p := strings.TrimSpace(a.Path)
	lower := strings.ToLower(p)
	switch {
	case p == "":
		return fmt.Errorf("%w: the disk path is empty", ErrRefused)
	case !(len(p) > 2 && p[1] == ':') && !strings.HasPrefix(p, `\\`):
		return fmt.Errorf("%w: %s is not an absolute Windows path; the task that applies the table runs from another directory", ErrRefused, p)
	case !strings.HasSuffix(lower, ".vhdx") && !strings.HasSuffix(lower, ".vhd"):
		return fmt.Errorf("%w: %s is not a .vhdx or .vhd; wsl --mount --vhd attaches only those", ErrRefused, p)
	case a.Bare && (a.Name != "" || a.Type != "" || a.Options != "" || a.Partition > 0):
		return fmt.Errorf("%w: --bare attaches the disk without mounting it, so --name, --type, --options and --partition mean nothing with it", ErrRefused)
	case a.Partition < 0:
		return fmt.Errorf("%w: --partition is a partition number, 1 or more", ErrRefused)
	case a.Name != "" && !validName.MatchString(a.Name):
		return fmt.Errorf("%w: %q is not a usable mount name: use letters, digits, '.', '-' and '_'", ErrRefused, a.Name)
	}
	// A distribution's own disk, attached a second time while that
	// distribution runs from it, is two kernels' worth of writes to one
	// ext4 filesystem.
	for _, r := range registered {
		if v := r.VhdPath(); v != "" && SamePath(v, p) {
			return fmt.Errorf("%w: %s is the disk of the distribution %s; attaching it as well would have two writers on one filesystem", ErrRefused, p, r.Name)
		}
	}
	for _, t := range table {
		if SamePath(t.Path, p) {
			return fmt.Errorf("%w: %s is already in the table", ErrRefused, p)
		}
	}
	return nil
}

// RemoveAutomount drops one entry by path.
func RemoveAutomount(table []AutomountEntry, path string) ([]AutomountEntry, bool) {
	var out []AutomountEntry
	found := false
	for _, t := range table {
		if SamePath(t.Path, path) {
			found = true
			continue
		}
		out = append(out, t)
	}
	return out, found
}

// Mounter attaches a disk. The production one is the disk Host's wsl.exe.
type Mounter interface {
	Mount(ctx context.Context, args []string) (CommandResult, error)
}

// AutomountOutcome is what happened to one entry.
type AutomountOutcome string

const (
	AutomountAttached AutomountOutcome = "attached"
	// AutomountAlreadyHeld means the file is open in something already,
	// which for a disk in this table is almost always WSL from an earlier
	// run. Attaching again would only fail.
	AutomountAlreadyHeld AutomountOutcome = "already attached or in use"
	AutomountMissing     AutomountOutcome = "missing"
	AutomountFailed      AutomountOutcome = "failed"
)

// AutomountResult is one entry's outcome.
type AutomountResult struct {
	Entry   AutomountEntry
	Outcome AutomountOutcome
	Detail  string
}

// ApplyAutomount attaches every entry that is not attached already. One disk
// that cannot be attached does not stop the rest.
//
// "Already attached" is decided by whether the file is held open, not by
// parsing what wsl.exe prints, so a localised Windows answers the same.
func ApplyAutomount(ctx context.Context, fs FileSystem, m Mounter, table []AutomountEntry) []AutomountResult {
	out := make([]AutomountResult, 0, len(table))
	for _, a := range table {
		res := AutomountResult{Entry: a}
		switch {
		case !fs.Exists(a.Path):
			res.Outcome, res.Detail = AutomountMissing, a.Path+" is not there"
		default:
			if locked, err := fs.Locked(a.Path); err == nil && locked {
				res.Outcome = AutomountAlreadyHeld
				break
			}
			r, err := m.Mount(ctx, a.MountArgs())
			switch {
			case err != nil:
				res.Outcome, res.Detail = AutomountFailed, err.Error()
			case r.ExitCode != 0:
				res.Outcome, res.Detail = AutomountFailed, fmt.Sprintf("wsl.exe exited %d: %s", r.ExitCode, oneLine(r.Stdout+" "+r.Stderr))
			default:
				res.Outcome = AutomountAttached
			}
		}
		out = append(out, res)
	}
	return out
}

// AutomountJSON is the object printed per entry.
func AutomountJSON(a AutomountEntry) map[string]any {
	o := map[string]any{"path": a.Path, "bare": a.Bare}
	if a.Name != "" {
		o["name"] = a.Name
	}
	if a.Type != "" {
		o["type"] = a.Type
	}
	if a.Options != "" {
		o["options"] = a.Options
	}
	if a.Partition > 0 {
		o["partition"] = a.Partition
	}
	return o
}
