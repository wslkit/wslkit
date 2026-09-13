package host

import (
	"testing"

	"github.com/wslkit/wslkit/internal/env"
)

const vhd = `C:\Users\Ana\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState\ext4.vhdx`

func TestExcludedByTheShapesPeopleActuallyWrite(t *testing.T) {
	profile := `C:\Users\Ana`
	cases := []struct {
		name string
		ex   env.Exclusions
		want bool
	}{
		{"the file itself", env.Exclusions{Paths: []string{vhd}}, true},
		{"differently cased", env.Exclusions{Paths: []string{`c:\users\ana\appdata\local\packages\canonicalgrouplimited.ubuntu_79rhkp1fndgsc\localstate\ext4.vhdx`}}, true},
		{"the folder above it", env.Exclusions{Paths: []string{`C:\Users\Ana\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState`}}, true},
		{"that folder with a trailing slash", env.Exclusions{Paths: []string{`C:\Users\Ana\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState\`}}, true},
		{"that folder with a trailing star", env.Exclusions{Paths: []string{`C:\Users\Ana\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState\*`}}, true},
		{"the whole Packages folder", env.Exclusions{Paths: []string{`C:\Users\Ana\AppData\Local\Packages`}}, true},
		{"written with an environment variable", env.Exclusions{Paths: []string{`%LOCALAPPDATA%\Packages`}}, true},
		{"written with %USERPROFILE%", env.Exclusions{Paths: []string{`%UserProfile%\AppData\Local\Packages`}}, true},
		{"a wildcard for every account", env.Exclusions{Paths: []string{`C:\Users\*\AppData\Local\Packages`}}, true},
		{"a wildcard for the package name", env.Exclusions{Paths: []string{`C:\Users\Ana\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_*\LocalState\ext4.vhdx`}}, true},
		{"by extension", env.Exclusions{Extensions: []string{"vhdx"}}, true},
		{"by extension written with a dot", env.Exclusions{Extensions: []string{".vhdx"}}, true},
		{"forward slashes", env.Exclusions{Paths: []string{`C:/Users/Ana/AppData/Local/Packages`}}, true},

		{"nothing at all", env.Exclusions{}, false},
		{"someone else's profile", env.Exclusions{Paths: []string{`C:\Users\Bob\AppData\Local\Packages`}}, false},
		{"a different extension", env.Exclusions{Extensions: []string{"vhd"}}, false},
		{"a prefix that is not a path component", env.Exclusions{Paths: []string{`C:\Users\An`}}, false},
		{"an unrelated folder", env.Exclusions{Paths: []string{`D:\wsl`}}, false},
		{"an empty rule", env.Exclusions{Paths: []string{"", "   "}}, false},
		{"a variable this cannot resolve", env.Exclusions{Paths: []string{`%SomethingElse%\Packages`}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule, got := ExcludedBy(vhd, c.ex, profile)
			if got != c.want {
				t.Fatalf("excludedBy = %v (%q), want %v", got, rule, c.want)
			}
			// The rule is reported back as the user wrote it, so they can
			// check it against what they think they set.
			if got && rule == "" {
				t.Error("matched, but did not say which rule matched")
			}
		})
	}
}

// A path as the registry spells it, which is where the VHDX path comes from.
func TestExcludedByHandlesTheExtendedLengthPrefix(t *testing.T) {
	if _, ok := ExcludedBy(`\\?\`+vhd, env.Exclusions{Paths: []string{`C:\Users\Ana\AppData\Local\Packages`}}, `C:\Users\Ana`); !ok {
		t.Error("a path with the \\\\?\\ prefix should still match")
	}
}

// A * stands for part of one name, not for the rest of the path: it is the
// difference between "every account's Packages folder" and "every folder
// anywhere called Packages". What makes the deep file match anyway is the other
// rule, that a folder exclusion is recursive.
func TestWildcardIsOneNameButFolderExclusionsAreRecursive(t *testing.T) {
	// C:\Users\* names each profile folder, and each of those is excluded
	// with everything under it.
	if _, ok := ExcludedBy(vhd, env.Exclusions{Paths: []string{`C:\Users\*`}}, ``); !ok {
		t.Error(`C:\Users\* names every profile folder, and a folder exclusion is recursive`)
	}
	// But the wildcard itself does not span separators: this one has too
	// many components to line up.
	if _, ok := ExcludedBy(vhd, env.Exclusions{Paths: []string{`C:\Users\*\Documents`}}, ``); ok {
		t.Error(`C:\Users\*\Documents should not cover a file that is not under Documents`)
	}
	if _, ok := ExcludedBy(`C:\wsl\Ubuntu\ext4.vhdx`, env.Exclusions{Paths: []string{`C:\wsl\*`}}, ``); !ok {
		t.Error(`C:\wsl\* should cover the folder below it`)
	}
}

func TestMatchSegment(t *testing.T) {
	yes := [][2]string{{"*", "anything"}, {"ubuntu_*", "ubuntu_79rhkp"}, {"ext?.vhdx", "ext4.vhdx"}, {"*.vhdx", "a.vhdx"}, {"a*b*c", "axxbyyc"}, {"exact", "exact"}}
	no := [][2]string{{"ubuntu_*", "debian_1"}, {"ext?.vhdx", "ext44.vhdx"}, {"a*b", "ab c"}, {"exact", "exacto"}}
	for _, c := range yes {
		if !matchSegment(c[0], c[1]) {
			t.Errorf("matchSegment(%q, %q) = false", c[0], c[1])
		}
	}
	for _, c := range no {
		if matchSegment(c[0], c[1]) {
			t.Errorf("matchSegment(%q, %q) = true", c[0], c[1])
		}
	}
}
