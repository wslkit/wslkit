package preflight

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslconfig"
	"github.com/wslkit/wslkit/internal/wslver"
)

// The checks below are the header-readable subset of Microsoft's own
// validate-modern.py, plus the one thing that script cannot know: which runtime
// this machine has. A distribution can be perfectly well built and still not
// start here, and that is the failure worth catching before the install rather
// than after it.

// distConfKeys are the keys WSL's init actually reads out of
// /etc/wsl-distribution.conf. Anything else in that file does nothing.
var distConfKeys = map[string]string{
	"oobe.command":                    "script run on first start, to create the default user",
	"oobe.defaultname":                "name the distribution registers under",
	"oobe.defaultuid":                 "uid the first shell runs as",
	"shortcut.enabled":                "create a Start menu shortcut",
	"shortcut.icon":                   "path inside the distribution to the shortcut icon",
	"shortcut.ico":                    "older spelling of shortcut.icon",
	"windowsterminal.enabled":         "add a Windows Terminal profile",
	"windowsterminal.profiletemplate": "path inside the distribution to the profile template",
}

// discouragedUnits are enabled systemd units that are known to misbehave under
// WSL. None of them stops a distribution from starting; each produces a failure
// somebody then spends an evening on.
var discouragedUnits = map[string]string{
	"systemd-resolved.service":             "WSL manages /etc/resolv.conf itself; resolved fights it and name resolution ends up depending on which won",
	"systemd-networkd.service":             "the network comes from the host, not from inside the distribution",
	"systemd-networkd-wait-online.service": "waits for an interface systemd did not configure, and delays every boot until it times out",
	"NetworkManager.service":               "same: the network is the host's",
	"systemd-tmpfiles-setup-dev.service":   "tries to create device nodes in a container that does not own them",
	"systemd-modules-load.service":         "there are no loadable modules in the WSL kernel unless one was built",
	"systemd-remount-fs.service":           "remounts filesystems the host mounted",
	"e2scrub_reap.service":                 "scrubs a filesystem that is a virtual disk file on the host",
}

// unsupportedXattrs do not survive a WSL 1 install.
var unsupportedXattrs = map[string]string{
	"security.selinux": "SELinux labels",
	"security.ima":     "IMA measurements",
	"security.evm":     "EVM signatures",
}

// Check returns the findings for one archive, given the machine it would be
// installed on. e may be nil, in which case the machine-specific checks say
// they were skipped rather than guessing.
func Check(a Archive, e *env.Env) []probe.Result {
	var out []probe.Result
	out = append(out, checkDistConf(a))
	out = append(out, checkDefaultUser(a))
	out = append(out, checkWslConf(a))
	out = append(out, checkUnits(a))
	out = append(out, checkXattrs(a))
	out = append(out, checkRuntime(a, e))
	return out
}

func base(id, title string) probe.Base {
	return probe.Base{PID: id, PTitle: title, PMilestone: "M2"}
}

// ---------------------------------------------------------------- PRE001

func checkDistConf(a Archive) probe.Result {
	b := base("PRE001", "wsl-distribution.conf")
	text, ok := a.Get("etc/wsl-distribution.conf")
	if !ok {
		// Not an error. Without it the distribution installs with WSL's
		// defaults, which is what every pre-modern tarball did.
		r := b.Res(probe.OK, 0.6, "No /etc/wsl-distribution.conf: this installs with WSL's defaults")
		r.Detail = "A modern distribution normally ships one, to set the first-run user and the Start menu entry. Without it the install works, runs as root, and creates no shortcut."
		return r
	}
	cfg := wslconfig.Parse(text)
	var findings, details []string
	if len(cfg.Problems) > 0 {
		p := cfg.Problems[0]
		findings = append(findings, fmt.Sprintf("line %d is malformed", p.Line))
		details = append(details, fmt.Sprintf("  line %d: %s  (%s)", p.Line, strings.TrimSpace(p.Raw), p.Msg))
	}
	for _, entry := range cfg.Entries {
		id := strings.ToLower(entry.Section) + "." + strings.ToLower(entry.Key)
		if _, known := distConfKeys[id]; !known {
			findings = append(findings, fmt.Sprintf("%s is not a key WSL reads", id))
			details = append(details, fmt.Sprintf("  line %d: %s is ignored. The keys WSL reads are: %s", entry.Line, id, strings.Join(sortedKeys(distConfKeys), ", ")))
			continue
		}
		if msg := distConfValue(id, entry.Value); msg != "" {
			findings = append(findings, fmt.Sprintf("%s: %s", id, msg))
			details = append(details, fmt.Sprintf("  line %d: %s", entry.Line, msg))
		}
	}
	if len(findings) == 0 {
		return b.Res(probe.OK, 0.7, fmt.Sprintf("wsl-distribution.conf is valid (%d setting(s))", len(cfg.Entries)))
	}
	r := b.Res(probe.Warn, 0.6, strings.Join(findings, "; "))
	r.Detail = strings.Join(details, "\n")
	return r
}

func distConfValue(id, value string) string {
	switch id {
	case "shortcut.enabled", "windowsterminal.enabled":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "false", "1", "0":
		default:
			return fmt.Sprintf("%q is not a boolean; WSL accepts true, false, 1 and 0", value)
		}
	case "oobe.defaultuid":
		if _, err := strconv.Atoi(strings.TrimSpace(value)); err != nil {
			return fmt.Sprintf("%q is not a number", value)
		}
	case "shortcut.icon", "shortcut.ico", "windowsterminal.profiletemplate":
		if !strings.HasPrefix(strings.TrimSpace(value), "/") {
			return fmt.Sprintf("%q is not an absolute path inside the distribution", value)
		}
	}
	return ""
}

// ---------------------------------------------------------------- PRE002

// checkDefaultUser is the rule that costs the most when it is wrong: the first
// shell opens as a user that does not exist, or as root when the distribution
// meant otherwise.
func checkDefaultUser(a Archive) probe.Result {
	b := base("PRE002", "Default user and /etc/passwd")
	passwd, hasPasswd := a.Get("etc/passwd")
	if !hasPasswd {
		return b.Res(probe.Warn, 0.5, "No /etc/passwd in the archive: every shell will run as an unnamed root")
	}
	users := parsePasswd(passwd)

	if name, ok := users[0]; ok && name != "root" {
		// uid 0 not being root breaks assumptions all the way down, from
		// sudo to the OOBE script WSL runs as root on first start.
		r := b.Res(probe.Fail, 0.8, fmt.Sprintf("uid 0 is %q, not root", name))
		r.Detail = "WSL runs the first-start command as uid 0 and so does nearly everything else. A distribution where uid 0 is not root behaves unpredictably from the first second."
		return r
	}

	text, ok := a.Get("etc/wsl-distribution.conf")
	if !ok {
		return b.Res(probe.OK, 0.6, fmt.Sprintf("/etc/passwd has %d user(s); no default user configured, so shells start as root", len(users)))
	}
	cfg := wslconfig.Parse(text)
	uid, set := "", false
	oobe := false
	for _, entry := range cfg.Entries {
		switch strings.ToLower(entry.Section) + "." + strings.ToLower(entry.Key) {
		case "oobe.defaultuid":
			uid, set = strings.TrimSpace(entry.Value), true
		case "oobe.command":
			oobe = true
		}
	}
	if !set {
		if oobe {
			// The usual shape: a first-run script creates the user, so the
			// uid is not in the archive and cannot be checked here.
			return b.Res(probe.OK, 0.5, "The default user is created by the first-run command, so it is not in the archive to check")
		}
		return b.Res(probe.OK, 0.6, "No default uid configured; shells start as root")
	}
	n, err := strconv.Atoi(uid)
	if err != nil {
		return b.Res(probe.Warn, 0.6, fmt.Sprintf("oobe.defaultUid is %q, which is not a number", uid))
	}
	if name, exists := users[n]; exists {
		return b.Res(probe.OK, 0.8, fmt.Sprintf("Default user is uid %d (%s), which exists in /etc/passwd", n, name))
	}
	if oobe {
		r := b.Res(probe.OK, 0.5, fmt.Sprintf("Default uid %d is not in /etc/passwd yet; the first-run command is expected to create it", n))
		r.Detail = "This is the normal shape for a distribution that asks for a username on first start. It is only a problem if that command fails, which WSL will not tell you about."
		return r
	}
	r := b.Res(probe.Fail, 0.8, fmt.Sprintf("Default uid %d does not exist in /etc/passwd and nothing creates it", n))
	r.Detail = "With no oobe.command to create the user, the first shell opens as a uid with no name, no home directory and no shell."
	return r
}

// parsePasswd reads uid to name out of an /etc/passwd.
func parsePasswd(text string) map[int]string {
	users := map[int]string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if len(fields) < 3 {
			continue
		}
		if uid, err := strconv.Atoi(fields[2]); err == nil {
			if _, dup := users[uid]; !dup {
				users[uid] = fields[0]
			}
		}
	}
	return users
}

// ---------------------------------------------------------------- PRE003

// checkWslConf lints the wsl.conf the archive ships, against the same table the
// live check uses.
func checkWslConf(a Archive) probe.Result {
	b := base("PRE003", "wsl.conf inside the archive")
	text, ok := a.Get("etc/wsl.conf")
	if !ok {
		return b.Res(probe.OK, 0.6, "No /etc/wsl.conf in the archive; every setting takes its default")
	}
	tbl, err := data.LoadWslConfKeys()
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "the wsl.conf key table could not be loaded: "+err.Error())
	}
	cfg := wslconfig.Parse(text)
	var findings, details []string
	if len(cfg.Problems) > 0 {
		p := cfg.Problems[0]
		// The same rule as the live check: WSL abandons the file at the
		// first line it cannot parse, so everything below is lost.
		lost := 0
		for _, entry := range cfg.Entries {
			if entry.Line > p.Line {
				lost++
			}
		}
		f := fmt.Sprintf("line %d is malformed", p.Line)
		if lost > 0 {
			f += fmt.Sprintf(", and the %d setting(s) below it will be ignored", lost)
		}
		findings = append(findings, f)
		details = append(details, fmt.Sprintf("  line %d: %s  (%s). WSL stops reading wsl.conf at the first line it cannot parse.", p.Line, strings.TrimSpace(p.Raw), p.Msg))
	}
	for _, entry := range cfg.Entries {
		if _, known := tbl.Lookup(entry.Section, entry.Key); !known {
			id := strings.ToLower(entry.Section) + "." + strings.ToLower(entry.Key)
			findings = append(findings, id+" is not a key WSL reads")
			details = append(details, fmt.Sprintf("  line %d: %s is ignored", entry.Line, id))
		}
	}
	if len(findings) == 0 {
		return b.Res(probe.OK, 0.7, fmt.Sprintf("wsl.conf is valid (%d setting(s))", len(cfg.Entries)))
	}
	r := b.Res(probe.Warn, 0.6, strings.Join(findings, "; "))
	r.Detail = strings.Join(details, "\n")
	return r
}

// ---------------------------------------------------------------- PRE004

func checkUnits(a Archive) probe.Result {
	b := base("PRE004", "Enabled systemd units")
	if !a.HasSystemd {
		return b.Res(probe.OK, 0.5, "The archive does not ship systemd")
	}
	var found, details []string
	for unit := range a.SystemdUnits {
		if why, bad := discouragedUnits[unit]; bad {
			found = append(found, unit)
			details = append(details, fmt.Sprintf("  %s: %s", unit, why))
		}
	}
	if len(found) == 0 {
		return b.Res(probe.OK, 0.6, fmt.Sprintf("No units known to misbehave under WSL are enabled (%d enabled)", len(a.SystemdUnits)))
	}
	sort.Strings(found)
	sort.Strings(details)
	r := b.Res(probe.Warn, 0.5, fmt.Sprintf("%d enabled unit(s) known to misbehave under WSL: %s", len(found), strings.Join(found, ", ")))
	r.Detail = strings.Join(details, "\n") + "\nNone of these stops the distribution from starting. Each produces a failure somebody then spends an evening on."
	r.FixHint = "systemctl disable <unit> inside the distribution after installing, or remove the .wants symlink from the archive"
	return r
}

// ---------------------------------------------------------------- PRE005

func checkXattrs(a Archive) probe.Result {
	b := base("PRE005", "Extended attributes")
	var found []string
	for attr := range a.Xattrs {
		if _, bad := unsupportedXattrs[attr]; bad {
			found = append(found, attr)
		}
	}
	if len(found) == 0 {
		return b.Res(probe.OK, 0.6, "No extended attributes that an install would drop")
	}
	sort.Strings(found)
	r := b.Res(probe.Warn, 0.5, fmt.Sprintf("Carries %s, which a WSL 1 install drops", strings.Join(found, ", ")))
	r.Detail = "WSL 2 keeps these. WSL 1 stores files on NTFS and cannot, so a distribution whose security model depends on them behaves differently there, silently."
	return r
}

// ---------------------------------------------------------------- PRE006

// checkRuntime is the check no distribution validator can do: whether this
// machine's WSL can install and start this distribution at all.
func checkRuntime(a Archive, e *env.Env) probe.Result {
	b := base("PRE006", "This machine's runtime")
	// An empty value counts as unknown: a zero-valued field reads as OK, and
	// an install check that guessed would be worse than one that abstains.
	if e == nil || !e.Runtime.Version.OK() || e.Runtime.Version.Value == "" {
		return b.Res(probe.Skipped, 0, "The installed WSL version is unknown, so this cannot say whether it would start")
	}
	cur, err := wslver.Parse(e.Runtime.Version.Value)
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "could not read the installed WSL version")
	}
	c, err := data.LoadCompat()
	if err != nil {
		return b.Res(probe.Unknown, 0.1, "the compatibility table could not be loaded: "+err.Error())
	}

	// The format itself. A .wsl file is only installable from 2.4.4.
	if min, perr := wslver.Parse(c.ModernFormatMin); perr == nil && cur.Less(min) {
		r := b.Res(probe.Fail, 0.9, fmt.Sprintf("WSL %s cannot install a .wsl file at all; %s is the first version that can", cur, min))
		r.Detail = "wsl --install --from-file does not exist on this runtime. The install fails before anything in the archive matters."
		r.FixHint = "wslkit doctor fix update"
		return r
	}

	// The one that actually catches people: a distribution whose systemd is
	// cgroup v2 only, on a runtime that still mounts the v1 hierarchy. It
	// installs, and then never boots.
	if v := systemdMajor(a.SystemdVersion); v >= 258 {
		if cap, ok := c.Capabilities["cgroup_v2"]; ok {
			if since, perr := wslver.Parse(cap.Since); perr == nil && cur.Less(since) {
				r := b.Res(probe.Fail, 0.9, fmt.Sprintf("systemd %s needs WSL %s or newer; this machine has %s", a.SystemdVersion, since, cur))
				r.Detail = cap.Description + "\nThe install will appear to succeed. The distribution will then fail to start, with an error that says nothing about cgroups."
				r.FixHint = "wslkit doctor fix update, or set automount.cgroups=v1 in the distribution's wsl.conf (WSL 2.6.2 and newer)"
				r.Refs = cap.Refs
				return r
			}
		}
	}

	msg := fmt.Sprintf("WSL %s can install this", cur)
	if a.SystemdVersion != "" {
		msg += fmt.Sprintf(" (archive ships systemd %s)", a.SystemdVersion)
	}
	return b.Res(probe.OK, 0.7, msg)
}

// systemdMajor reads the major version out of a systemd version string, which
// looks like "257.4-1" or "258".
func systemdMajor(v string) int {
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
