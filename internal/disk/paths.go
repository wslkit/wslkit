package disk

import "strings"

// CanonicalPath reduces a Windows path to a form two spellings of the same file
// share, so they can be compared.
//
// This is the whole comparison primitive for deciding whether a disk is claimed
// by a registration. It has to cope with what is actually stored: the
// extended-length prefix appears on some registrations and not others, forward
// slashes are legal in Win32 paths and do turn up in hand-edited registry
// values, and case never matters.
//
// It is not a resolution: no symlink is followed and no relative path is made
// absolute. It answers "are these the same spelling of a path", which is the
// question the orphan scan asks.
func CanonicalPath(p string) string {
	// The UNC form starts with the plain prefix, so it must be tested first
	// or stripping only the shorter one leaves UNC\server\share behind.
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		p = `\\` + strings.TrimPrefix(p, `\\?\UNC\`)
	case strings.HasPrefix(p, `\\?\`):
		p = strings.TrimPrefix(p, `\\?\`)
	}
	p = strings.ReplaceAll(p, "/", `\`)
	// A trailing separator is not part of the identity, but a bare root is.
	for len(p) > 1 && strings.HasSuffix(p, `\`) && !strings.HasSuffix(p, `:\`) {
		p = strings.TrimSuffix(p, `\`)
	}
	return strings.ToLower(p)
}

// SamePath reports whether two spellings name the same file.
func SamePath(a, b string) bool {
	return CanonicalPath(a) == CanonicalPath(b)
}

// DirOf returns the directory part of a Windows path, without the host's idea
// of a separator getting involved.
func DirOf(p string) string {
	p = strings.TrimRight(p, `\/`)
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return ""
	}
	if i == 2 && len(p) > 2 && p[1] == ':' {
		// C:\file -> C:\
		return p[:3]
	}
	return p[:i]
}

// BaseOf returns the final component of a Windows path.
func BaseOf(p string) string {
	p = strings.TrimRight(p, `\/`)
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}

// HasExtendedPrefix reports whether a stored path carries the extended-length
// prefix, so a rewritten value can keep whichever form it had.
func HasExtendedPrefix(p string) bool {
	return strings.HasPrefix(p, `\\?\`)
}

// WithSamePrefixAs returns dir carrying the extended-length prefix if, and only
// if, the original value had it.
//
// Normalising it either way would be an unrequested change to a value the tool
// does not own: Docker Desktop writes the prefixed form and WSL writes the bare
// one, on the same machine.
func WithSamePrefixAs(original, dir string) string {
	bare := strings.TrimPrefix(dir, `\\?\`)
	if HasExtendedPrefix(original) {
		return `\\?\` + bare
	}
	return bare
}
