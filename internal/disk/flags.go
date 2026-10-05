package disk

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A distribution's Flags value on its Lxss key holds three switches WSL reads
// as the distribution starts: interop (launching Windows programs), appending
// the Windows PATH, and mounting the Windows drives. wsl.exe has no command for
// any of them, so turning interop off for one distribution has meant editing
// the registry by hand (#92). WSL also sets a fourth bit, 0x8, that it does not
// document; it is never changed here, only carried over.

// FlagChange is what to set. A nil field leaves that switch alone.
type FlagChange struct {
	Interop    *bool
	AppendPath *bool
	Automount  *bool
}

// Empty reports whether there is nothing to change.
func (c FlagChange) Empty() bool {
	return c.Interop == nil && c.AppendPath == nil && c.Automount == nil
}

// ParseOnOff reads a switch the way the flags are written: on or off, and
// the true/false spelling for anyone who reaches for it.
func ParseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true":
		return true, nil
	case "off", "false":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not on or off", s)
	}
}

// ApplyFlags returns the new Flags value. Every bit it was not asked about,
// the undocumented one included, is kept exactly as it was.
func ApplyFlags(old int, c FlagChange) int {
	out := old
	for _, s := range []struct {
		want *bool
		bit  int
	}{{c.Interop, FlagInterop}, {c.AppendPath, FlagAppendNTPath}, {c.Automount, FlagDriveMount}} {
		if s.want == nil {
			continue
		}
		if *s.want {
			out |= s.bit
		} else {
			out &^= s.bit
		}
	}
	return out
}

// onOff renders one bit for the report.
func onOff(flags, bit int) string {
	if flags&bit != 0 {
		return "on"
	}
	return "off"
}

// FlagLines describes a Flags value one switch per line, in the words the
// command's flags use.
func FlagLines(flags int) []string {
	lines := []string{
		"interop:      " + onOff(flags, FlagInterop),
		"append-path:  " + onOff(flags, FlagAppendNTPath),
		"automount:    " + onOff(flags, FlagDriveMount),
	}
	if rest := flags &^ (FlagInterop | FlagAppendNTPath | FlagDriveMount); rest != 0 {
		lines = append(lines, fmt.Sprintf("undocumented: 0x%x (left as it is)", rest))
	}
	return lines
}

// FlagsJSON is the object printed for --json.
func FlagsJSON(name string, flags int) map[string]any {
	return map[string]any{
		"distribution": name,
		"flags":        flags,
		"interop":      flags&FlagInterop != 0,
		"append_path":  flags&FlagAppendNTPath != 0,
		"automount":    flags&FlagDriveMount != 0,
	}
}

// PlanFlags describes changing the switches.
func PlanFlags(r Registration, c FlagChange, running bool) (Plan, int, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if c.Empty() {
		return p, r.Flags, fmt.Errorf("%w: name at least one of --interop, --append-path or --automount", ErrRefused)
	}
	next := ApplyFlags(r.Flags, c)
	if next == r.Flags {
		return p, next, fmt.Errorf("%w: %s already has those settings", ErrRefused, r.Name)
	}
	p.AddUndoable("set Flags on %s from %d to %d (%s)", r.Name, r.Flags, next, DecodeFlags(next))
	if c.Interop != nil && !*c.Interop {
		// Measured on 3.0.1: with the bit off, sessions get no WSL_INTEROP
		// socket of their own, but a Windows program still starts through
		// /run/WSL/1_interop. wsl.conf's [interop] enabled=false is what
		// makes it fail.
		p.Warn("on WSL 3.0.1 this bit does not stop Windows programs launching: sessions lose their own interop socket, and cmd.exe still starts",
			"to block them, put [interop] enabled=false in the distribution's /etc/wsl.conf")
	}
	if running {
		p.Warn(r.Name+" is running, and WSL reads these as it starts",
			"they take effect after wsl --terminate "+r.Name)
	}
	return p, next, nil
}

// ParseUID reads a numeric user id.
func ParseUID(s string) (uint32, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// userLookupTimeout bounds asking a running distribution who a user is.
const userLookupTimeout = 30 * time.Second

// LookupUser resolves a user inside a running distribution: a name to its uid,
// or a uid to its name, which proves the account exists.
//
// It is only ever asked of a distribution that is already up. Starting one to
// look up a user would change the thing being configured, so a stopped
// distribution takes a number and is told it was not checked.
func LookupUser(ctx context.Context, e Env, distro, user string) (uid uint32, name string, err error) {
	// The name goes in as an argument, not into the script, so nothing in it
	// is ever read as shell.
	res, err := e.Host.RunAsRoot(ctx, distro, []string{"/bin/sh", "-c", `id -u "$1" && id -nu "$1"`, "sh", user}, userLookupTimeout)
	if err != nil {
		return 0, "", err
	}
	if res.ExitCode != 0 {
		return 0, "", fmt.Errorf("%w: %s has no user %q", ErrRefused, distro, user)
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) < 2 {
		return 0, "", fmt.Errorf("disk: looking up %q in %s printed %q", user, distro, oneLine(res.Stdout))
	}
	n, ok := ParseUID(fields[0])
	if !ok {
		return 0, "", fmt.Errorf("disk: looking up %q in %s printed %q", user, distro, oneLine(res.Stdout))
	}
	return n, fields[1], nil
}
