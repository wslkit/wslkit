package host

import (
	"strings"

	"github.com/wslkit/wslkit/internal/env"
)

// Whether Defender is scanning a distribution's disk comes down to matching one
// path against the exclusion list, and the list is not a list of plain paths.
//
// Every published recipe for excluding a WSL disk writes them differently:
// `%USERPROFILE%\...` from a documentation page, `C:\Users\*\AppData\...` from
// a script that has to work for every account, `C:\wsl\*` from someone who
// excluded the whole folder, and the extension list from someone who excluded
// `vhdx` everywhere instead. Matching only the literal string means telling a
// reader who did exclude their disk that they did not, which is worse than not
// checking: they go and add a second, redundant exclusion.

// ExcludedBy reports which exclusion covers a path, if any. The returned string
// is the rule as the user wrote it, which is what makes the answer checkable.
//
// Exported because the fix has to answer the same question before adding a
// rule that is already covered by one of these.
func ExcludedBy(path string, ex env.Exclusions, userProfile string) (string, bool) {
	p := normalizePath(path)
	if p == "" {
		return "", false
	}

	// An extension exclusion covers the file wherever it lives, which is how
	// most of the "exclude vhdx" advice is written.
	for _, e := range ex.Extensions {
		e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e), ".")))
		if e != "" && strings.HasSuffix(p, "."+e) {
			return "extension " + e, true
		}
	}

	for _, rule := range ex.Paths {
		x := normalizePath(expandVars(rule, userProfile))
		if x == "" {
			continue
		}
		// A trailing star is how people write "and everything under it",
		// which is what a folder exclusion means anyway.
		x = strings.TrimSuffix(strings.TrimSuffix(x, `\*`), `\`)
		if matchWindowsPath(x, p) {
			return rule, true
		}
	}
	return "", false
}

// normalizePath puts a path in the form the comparison expects: lower case,
// without the extended-length prefix, and without a trailing separator.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, `\\?\`)
	p = strings.ToLower(p)
	// A forward slash is accepted everywhere on Windows, and someone typing
	// an exclusion by hand does sometimes use one.
	p = strings.ReplaceAll(p, "/", `\`)
	if len(p) > 3 {
		p = strings.TrimSuffix(p, `\`)
	}
	return p
}

// expandVars resolves the environment variables that turn up in an exclusion
// written by a documentation page rather than by the Defender UI.
//
// Only the ones derivable from the profile directory are resolved, because that
// is all a snapshot holds. An exclusion naming anything else is left as it is
// and will not match, which is the right way round: a rule this cannot read is
// reported as not covering the disk, not as covering it.
func expandVars(s, userProfile string) string {
	if !strings.Contains(s, "%") || userProfile == "" {
		return s
	}
	up := strings.TrimSuffix(userProfile, `\`)
	drive := ""
	if len(up) >= 2 && up[1] == ':' {
		drive = up[:2]
	}
	for _, kv := range [][2]string{
		{"%userprofile%", up},
		{"%localappdata%", up + `\AppData\Local`},
		{"%appdata%", up + `\AppData\Roaming`},
		{"%homepath%", strings.TrimPrefix(up, drive)},
		{"%systemdrive%", drive},
	} {
		if kv[1] == "" {
			continue
		}
		s = replaceFold(s, kv[0], kv[1])
	}
	return s
}

// replaceFold replaces every case-insensitive occurrence of old.
func replaceFold(s, old, new string) string {
	var sb strings.Builder
	ls, lo := strings.ToLower(s), strings.ToLower(old)
	for {
		i := strings.Index(ls, lo)
		if i < 0 {
			sb.WriteString(s)
			return sb.String()
		}
		sb.WriteString(s[:i])
		sb.WriteString(new)
		s, ls = s[i+len(old):], ls[i+len(old):]
	}
}

// matchWindowsPath reports whether an exclusion covers a path.
//
// Two rules, both Defender's. A rule that names a folder covers everything
// under it, however deep, so matching the rule against the leading components
// of the path is enough. And a wildcard stands for part of one name: `*` is any
// run of characters and `?` is one, and neither crosses a separator, so
// `C:\Users\*\AppData\Local\Packages` is every account's package folder and not
// every folder called Packages anywhere.
//
// Both arguments are already normalized.
func matchWindowsPath(pattern, path string) bool {
	pp := strings.Split(pattern, `\`)
	sp := strings.Split(path, `\`)
	if len(pp) == 0 || len(sp) < len(pp) {
		return false
	}
	for i := range pp {
		if !matchSegment(pp[i], sp[i]) {
			return false
		}
	}
	return true
}

// matchSegment matches one path component against one pattern component.
func matchSegment(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == s
	}
	// A small backtracking matcher. The patterns here are a handful of
	// characters long, so nothing cleverer is called for.
	var match func(p, v string) bool
	match = func(p, v string) bool {
		for {
			switch {
			case p == "":
				return v == ""
			case p[0] == '*':
				for i := 0; i <= len(v); i++ {
					if match(p[1:], v[i:]) {
						return true
					}
				}
				return false
			case v == "":
				return false
			case p[0] == '?' || p[0] == v[0]:
				p, v = p[1:], v[1:]
			default:
				return false
			}
		}
	}
	return match(pattern, s)
}
