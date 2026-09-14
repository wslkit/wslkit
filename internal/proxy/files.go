package proxy

import (
	"fmt"
	"sort"
	"strings"
)

// Setting a proxy inside a distribution is not one file. Each of the places
// below is read by something that reads none of the others, and a configuration
// that covers three of them is the one that works for apt and not for pip, or
// for a login shell and not for the systemd unit doing the actual work.
//
// Everything written carries a marked block, so reverting is exact: the lines
// between the markers go and the rest of the file is left as it was. A user's
// own /etc/environment entries are not this tool's to rewrite.

const (
	// BeginMarker and EndMarker bracket every managed block. The version is
	// not in them on purpose: a later release has to be able to find and
	// replace what an earlier one wrote.
	BeginMarker = "# >>> wslkit proxy >>>"
	EndMarker   = "# <<< wslkit proxy <<<"
	// warning is repeated inside each block, because the file it lands in is
	// one somebody will open in an editor months later with no memory of
	// this command.
	warning = "# Written by wslkit proxy apply. Edits between these markers are lost on the next apply.\n# Remove with: wslkit proxy revert -d <distro>"
)

// EnvFile is the one file everything else points at.
//
// A systemd unit can watch it, a script can source it, and a later apply
// rewrites one file rather than hunting through five. It answers
// microsoft/WSL#14152, which asks for exactly this and has no answer upstream.
const EnvFile = "/etc/wslkit/proxy.env"

// File is one file to write inside the distribution.
type File struct {
	// Path is absolute, inside the distribution.
	Path string
	// Content is the whole file when Whole is true, and the managed block to
	// merge into it otherwise.
	Content string
	// Whole means the file belongs to wslkit entirely: writing it replaces
	// whatever was there, and reverting deletes it.
	Whole bool
	// Why explains, in one line, what reads this file. It is printed by the
	// dry run, which is the only chance anybody gets to object.
	Why string
	// Mode is the octal permission for a file created fresh.
	Mode string
	// OwnsDir means the directory holding this file belongs to wslkit too,
	// so reverting takes it away when nothing else is left in it. Leaving an
	// empty /etc/wslkit behind is the kind of litter that makes a user
	// wonder what else was left.
	OwnsDir bool
}

// Files renders everything a distribution needs for these settings.
//
// The order is deliberate: the environment file first, because the rest refer
// to it, and the systemd drop-in last, because it is the one that needs a
// daemon-reload to take effect and the note about that should be the last thing
// read.
func Files(s Settings) []File {
	if s.Empty() {
		return nil
	}
	vars := s.Env()

	var out []File
	out = append(out, File{
		Path:    EnvFile,
		Whole:   true,
		Mode:    "0644",
		OwnsDir: true,
		Why:     "the one file the others point at, and the one a systemd unit can watch",
		Content: strings.Join([]string{
			"# Written by wslkit proxy apply.",
			"# The proxy for this distribution, in one place.",
			"# A unit can pick it up with EnvironmentFile=" + EnvFile + ".",
			renderKV(vars, ""),
		}, "\n"),
	})

	// /etc/environment is read by PAM, so it reaches every login session and
	// every su. It is not a shell script: no export, no expansion, no
	// comments in some implementations, so the block is plain KEY=value.
	out = append(out, File{
		Path: "/etc/environment",
		Mode: "0644",
		Why:  "read by PAM, so every login session and every su gets it",
		Content: strings.Join([]string{
			warning,
			renderKV(vars, ""),
		}, "\n"),
	})

	// A login shell reads profile.d; this is what makes an interactive
	// terminal work, and it is the only one WSL's own injection already
	// covers.
	out = append(out, File{
		Path:  "/etc/profile.d/99-wslkit-proxy.sh",
		Whole: true,
		Mode:  "0644",
		Why:   "read by every login shell",
		Content: strings.Join([]string{
			"# Written by wslkit proxy apply.",
			"# Sourced by every login shell. Remove with: wslkit proxy revert",
			renderKV(vars, "export "),
		}, "\n"),
	})

	// apt does not read the environment when it runs from a systemd timer,
	// and its own configuration language is not shell.
	out = append(out, File{
		Path:  "/etc/apt/apt.conf.d/99wslkit-proxy",
		Whole: true,
		Mode:  "0644",
		Why:   "apt does not read the environment when it runs from a timer",
		Content: strings.Join([]string{
			"// Written by wslkit proxy apply. Remove with: wslkit proxy revert",
			fmt.Sprintf("Acquire::http::Proxy %q;", s.HTTP),
			fmt.Sprintf("Acquire::https::Proxy %q;", s.HTTPS),
			// A trailing newline, because a configuration file without one
			// is a file somebody's editor will silently change.
			"",
		}, "\n"),
	})

	// The gap that costs the most. A systemd service starts with an
	// environment systemd builds, not the one the shell had, so a machine
	// where curl works and the unit calling curl does not is the normal
	// shape of this problem.
	out = append(out, File{
		Path:  "/etc/systemd/system.conf.d/wslkit-proxy.conf",
		Whole: true,
		Mode:  "0644",
		Why:   "systemd builds its own environment; without this no unit is proxied",
		Content: strings.Join([]string{
			"# Written by wslkit proxy apply. Remove with: wslkit proxy revert",
			"# Applies to system units. systemctl daemon-reexec makes it take effect,",
			"# and already-running units keep the environment they started with.",
			"[Manager]",
			renderSystemd(vars),
		}, "\n"),
	})

	return out
}

// renderKV renders the variables one per line, with an optional prefix.
func renderKV(vars []EnvVar, prefix string) string {
	var b strings.Builder
	for _, v := range sortVars(vars) {
		fmt.Fprintf(&b, "%s%s=%s\n", prefix, v.Name, quoteIfNeeded(v.Value))
	}
	return b.String()
}

// renderSystemd writes the DefaultEnvironment line systemd wants: one
// directive, every assignment on it, each quoted.
func renderSystemd(vars []EnvVar) string {
	parts := make([]string, 0, len(vars))
	for _, v := range sortVars(vars) {
		parts = append(parts, fmt.Sprintf("%q", v.Name+"="+v.Value))
	}
	return "DefaultEnvironment=" + strings.Join(parts, " ") + "\n"
}

// sortVars puts the variables in a stable order, lower case first so a diff
// between two applies is readable.
func sortVars(vars []EnvVar) []EnvVar {
	out := append([]EnvVar(nil), vars...)
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// quoteIfNeeded quotes a value that would otherwise be read wrongly.
//
// A proxy URL has no spaces in practice, but a no_proxy list that grew a space
// would silently truncate everything after it in a shell file, and the failure
// would look like the proxy working for some hosts and not others.
func quoteIfNeeded(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"'$`\\") {
		return fmt.Sprintf("%q", v)
	}
	return v
}

// Merge puts a managed block into an existing file, replacing any block already
// there and leaving everything else alone.
//
// This is what makes revert exact and re-apply idempotent. A tool that appends
// to /etc/environment every time it runs leaves a file with six copies of the
// same variable, and the user cannot tell which one is live.
func Merge(existing, block string) string {
	block = strings.TrimRight(block, "\n")
	managed := BeginMarker + "\n" + block + "\n" + EndMarker + "\n"

	before, rest, found := strings.Cut(existing, BeginMarker)
	if !found {
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		return existing + managed
	}
	// Everything after the end marker is kept: a user may well have added
	// their own lines below the block.
	_, after, ok := strings.Cut(rest, EndMarker)
	if !ok {
		// A truncated block, from an interrupted write or a hand edit. The
		// remainder is not recoverable as anything but ours, so it goes.
		return before + managed
	}
	after = strings.TrimPrefix(after, "\n")
	return before + managed + after
}

// Unmerge removes the managed block, returning the file as it was before and
// whether anything was found.
func Unmerge(existing string) (string, bool) {
	before, rest, found := strings.Cut(existing, BeginMarker)
	if !found {
		return existing, false
	}
	_, after, ok := strings.Cut(rest, EndMarker)
	if !ok {
		return before, true
	}
	return before + strings.TrimPrefix(after, "\n"), true
}
