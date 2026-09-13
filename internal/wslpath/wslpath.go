// Package wslpath translates names between how Linux spells them inside a
// distribution and how Windows sees them over \\wsl.localhost.
package wslpath

import "strings"

// A Linux filename may contain characters Windows forbids in a name: a colon,
// an asterisk, a question mark, a backslash and the rest. WSL's file server does
// not refuse them and does not drop them. It shifts each one into the Unicode
// private use area, at U+F000 plus the character, which is the same trick
// Services for UNIX used and which Windows will happily store.
//
// So a Linux file called "a:b" is a Windows file called "ab". The two look
// identical in a terminal, compare unequal in code, and only one of them opens.
//
// This matters most for the file at the centre of it all. Saving a download
// into a distribution leaves "name:Zone.Identifier" on the Linux side, which
// reaches Windows as "nameZone.Identifier". Anything looking for a colon
// finds nothing and reports a clean tree.

// escaped is the offset WSL adds. The characters it applies to are exactly
// those Windows will not accept in a name.
const escaped = 0xF000

const forbidden = `"*:<>?\|`

// ToLinux turns a name as Windows sees it into the name Linux gave it.
func ToLinux(name string) string {
	if !strings.ContainsFunc(name, isEscaped) {
		return name
	}
	return strings.Map(func(r rune) rune {
		if isEscaped(r) {
			return r - escaped
		}
		return r
	}, name)
}

// ToWindows turns a name Linux gave a file into the name Windows sees.
//
// This is not cosmetic: passing the Linux spelling to a Windows call does not
// open the same file. A colon in particular is read as the start of an
// alternate data stream, so "a:Zone.Identifier" addresses a stream of the file
// "a" rather than the file next to it.
func ToWindows(name string) string {
	if !strings.ContainsAny(name, forbidden) {
		return name
	}
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(forbidden, r) {
			return r + escaped
		}
		return r
	}, name)
}

// isEscaped reports whether a rune is one WSL shifted out of the way.
func isEscaped(r rune) bool {
	return r > escaped && r < escaped+0x80 && strings.ContainsRune(forbidden, r-escaped)
}

// UNC builds the Windows path that reaches a path inside a distribution.
//
// linuxPath is absolute and slash-separated, as it is written inside the
// distribution. Nothing here uses filepath: these are Windows paths whether or
// not the code is running on Windows, and the tests run on Linux too.
func UNC(distro, linuxPath string) string {
	var sb strings.Builder
	sb.WriteString(`\\wsl.localhost\`)
	sb.WriteString(distro)
	for _, part := range strings.Split(linuxPath, "/") {
		if part == "" {
			continue
		}
		sb.WriteString(`\`)
		sb.WriteString(ToWindows(part))
	}
	return sb.String()
}
