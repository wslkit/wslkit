package wslpath

import "testing"

// esc is the private use character WSL stores in place of c.
func esc(c rune) string { return string(rune(escaped + c)) }

// The escaped colon is the whole reason this package exists: measured against a
// running distribution, a Linux file called "sample.iso:Zone.Identifier" is
// listed by Windows as "sample.iso<U+F03A>Zone.Identifier".
func TestColonRoundTrip(t *testing.T) {
	linux := "sample.iso:Zone.Identifier"
	win := "sample.iso" + esc(':') + "Zone.Identifier"
	if got := ToWindows(linux); got != win {
		t.Errorf("ToWindows(%q) = %q, want %q", linux, got, win)
	}
	if got := ToLinux(win); got != linux {
		t.Errorf("ToLinux(%q) = %q", win, got)
	}
}

func TestEveryForbiddenCharacter(t *testing.T) {
	linux := `a"b*c:d<e>f?g\h|i`
	win := ToWindows(linux)
	for _, r := range forbidden {
		if want := esc(r); !contains(win, want) {
			t.Errorf("%q was not escaped in %q", r, win)
		}
	}
	if got := ToLinux(win); got != linux {
		t.Errorf("round trip = %q, want %q", got, linux)
	}
}

// A name with nothing to escape must come back byte for byte, which is the
// common case and the one worth being sure about.
func TestOrdinaryNamesAreUntouched(t *testing.T) {
	for _, s := range []string{"", "notes.txt", "Downloads", "ünïcödé.pdf", "a.b.c"} {
		if got := ToWindows(s); got != s {
			t.Errorf("ToWindows(%q) = %q", s, got)
		}
		if got := ToLinux(s); got != s {
			t.Errorf("ToLinux(%q) = %q", s, got)
		}
	}
}

// A private use character that is not one of WSL's is somebody's filename, and
// mangling it would be worse than leaving it alone.
func TestUnrelatedPrivateUseCharacterIsLeftAlone(t *testing.T) {
	s := "logo" + string(rune(0xF100)) + ".png"
	if got := ToLinux(s); got != s {
		t.Errorf("ToLinux(%q) = %q", s, got)
	}
}

func TestUNC(t *testing.T) {
	got := UNC("Ubuntu", "/home/ana/Downloads/a.exe:Zone.Identifier")
	want := `\\wsl.localhost\Ubuntu\home\ana\Downloads\a.exe` + esc(':') + "Zone.Identifier"
	if got != want {
		t.Errorf("UNC = %q, want %q", got, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
