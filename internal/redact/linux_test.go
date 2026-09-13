package redact

import "testing"

// Checks that read inside a distribution report paths from inside it, and a
// Linux home directory names a person exactly as plainly as C:\Users does.
func TestLinuxHomeIsScrubbed(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/ana/Downloads/a.exe:Zone.Identifier", "/home/<user>/Downloads/a.exe:Zone.Identifier"},
		{`"/home/ana"`, `"/home/<user>"`},
		{"/mnt/c/Users/Ana/src/app", "/mnt/c/Users/<user>/src/app"},
		{`\\wsl.localhost\Ubuntu\home\ana\x`, `\\wsl.localhost\Ubuntu\home\ana\x`}, // Windows spelling, left to the Windows rules
	}
	for _, c := range cases {
		if got := String(c.in, Rules{}); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// /root is not a name, and neither is the directory itself. Scrubbing either
// would make a report harder to read for nothing.
func TestLinuxPathsThatAreNotNames(t *testing.T) {
	for _, s := range []string{"/root/a.iso", "/home/", "/homebrew/bin", "/mnt/c/Windows/System32"} {
		if got := String(s, Rules{}); got != s {
			t.Errorf("String(%q) = %q, want it unchanged", s, got)
		}
	}
}
