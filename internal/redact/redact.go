// Package redact scrubs user-identifying strings from output destined for
// public bug reports: profile paths, SIDs, hostnames.
package redact

import (
	"regexp"
	"strings"
)

type Rules struct {
	UserProfile string // e.g. C:\Users\jane
	Hostname    string
}

var (
	reUsersPath = regexp.MustCompile(`(?i)([A-Z]:\\+Users\\+)([^\\/"'\s]+)`)
	reUsersJSON = regexp.MustCompile(`(?i)([A-Z]:\\\\Users\\\\)([^\\/"'\s]+)`)
	reSID       = regexp.MustCompile(`S-1-5-21-\d+-\d+-\d+-\d+`)
	// Paths inside a distribution reach the output too: a check that walks
	// \\wsl.localhost reports what it found there, and /home/<name> names a
	// person as plainly as C:\Users\<name> does.
	reLinuxHome = regexp.MustCompile(`(/home/)([^/"'\s\\]+)`)
	// A Windows drive as Linux sees it. The Windows rules above do not fire
	// on it, because by then the path is /mnt/c/Users/<name>.
	reMntUsers = regexp.MustCompile(`(?i)(/mnt/[a-z]/Users/)([^/"'\s\\]+)`)
)

// String applies all rules. Safe to run on JSON (handles escaped backslashes) and on text.
func String(s string, r Rules) string {
	if r.UserProfile != "" {
		s = strings.ReplaceAll(s, r.UserProfile, `%USERPROFILE%`)
		s = strings.ReplaceAll(s, strings.ReplaceAll(r.UserProfile, `\`, `\\`), `%USERPROFILE%`)
		s = strings.ReplaceAll(s, strings.ReplaceAll(r.UserProfile, `\`, `/`), `%USERPROFILE%`)
	}
	s = reUsersJSON.ReplaceAllString(s, `${1}<user>`)
	s = reUsersPath.ReplaceAllString(s, `${1}<user>`)
	s = reSID.ReplaceAllString(s, `S-1-5-21-<redacted>`)
	s = reMntUsers.ReplaceAllString(s, `${1}<user>`)
	s = reLinuxHome.ReplaceAllString(s, `${1}<user>`)
	if r.Hostname != "" && len(r.Hostname) >= 3 {
		s = replaceWord(s, r.Hostname, "<host>")
	}
	return s
}

// replaceWord replaces case-insensitive whole-token occurrences.
func replaceWord(s, word, with string) string {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
	return re.ReplaceAllString(s, with)
}
