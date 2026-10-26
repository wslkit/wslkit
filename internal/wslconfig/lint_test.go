package wslconfig

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/wslver"
)

func table(t *testing.T) *data.ConfigKeys {
	t.Helper()
	k, err := data.LoadConfigKeys()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// find returns the finding for key whose message contains frag (a key can
// carry several findings, e.g. "undocumented" info plus a "bridged" warning).
func find(fs []Finding, key, frag string) *Finding {
	for i := range fs {
		if strings.EqualFold(fs[i].Key, key) && strings.Contains(fs[i].Message, frag) {
			return &fs[i]
		}
	}
	return nil
}

func TestLintRules(t *testing.T) {
	cfg := Parse(`memory=2GB
[wsl2]
memory=64GB
procesors=4
autoMemoryReclaim=gradual
kernel=C:\\nope\\kernel
networkingMode=mirrored
localhostForwarding=true
vmSwitch=Default Switch
nestedVirtualization=maybe
[experimental]
sparseVhd=true
`)
	host := Host{WindowsBuild: 19045, Runtime: wslver.MustParse("2.7.13"), TotalRAM: 16 << 30, PathExists: func(string) bool { return false }}
	fs := Lint(cfg, table(t), host)

	cases := map[string]struct {
		sev  Severity
		frag string
	}{
		"memory":                    {Warn, "before any [section]"},
		"wsl2.procesors":            {Warn, "unknown key"},
		"wsl2.autoMemoryReclaim":    {Warn, "belongs under [experimental]"},
		"wsl2.kernel":               {Fail, "cannot start"},
		"wsl2.localhostForwarding":  {Info, "ignored with networkingMode=mirrored"},
		"wsl2.vmSwitch":             {Warn, "bridged"},
		"wsl2.nestedVirtualization": {Warn, "not a boolean"},
	}
	for key, want := range cases {
		f := find(fs, key, want.frag)
		if f == nil {
			t.Errorf("no finding for %s containing %q\n%+v", key, want.frag, fs)
			continue
		}
		if f.Severity != want.sev {
			t.Errorf("%s: got %s %q, want %s", key, f.Severity, f.Message, want.sev)
		}
	}
	// memory=64GB on a 16 GB host, and Windows-11-only keys on Windows 10, and mirrored on Win10.
	var mem, win11, mirroredFail int
	for _, f := range fs {
		if strings.EqualFold(f.Key, "wsl2.memory") && strings.Contains(f.Message, "exceeds") {
			mem++
		}
		if strings.Contains(f.Message, "needs Windows build 22000") {
			win11++
		}
		if f.Severity == Fail && strings.Contains(f.Message, "22H2") {
			mirroredFail++
		}
	}
	if mem != 1 || win11 < 2 || mirroredFail != 1 {
		t.Errorf("mem=%d win11=%d mirroredFail=%d\n%+v", mem, win11, mirroredFail, fs)
	}
	// Findings are sorted by line.
	for i := 1; i < len(fs); i++ {
		if fs[i].LineNo < fs[i-1].LineNo {
			t.Fatal("findings not sorted by line")
		}
	}
}

func TestLintCleanFile(t *testing.T) {
	cfg := Parse("[wsl2]\nmemory=4GB\nprocessors=2\n\n[experimental]\nautoMemoryReclaim=gradual\nsparseVhd=true\n")
	// 3.0.4: before 3.0.2 sparseVhd is ignored, which is a finding of its own.
	host := Host{WindowsBuild: 26100, Runtime: wslver.MustParse("3.0.4"), TotalRAM: 32 << 30}
	for _, f := range Lint(cfg, table(t), host) {
		if f.Severity != Info {
			t.Errorf("unexpected %s: %+v", f.Severity, f)
		}
	}
}

// #82: consomme is a valid networkingMode from 2.9.3, and virtioproxy is its
// former name, still an alias in 3.0.1. Neither may be a warning: the fix for
// a WSL005 warning comments the line out, which would turn a working setting
// into the default.
func TestLintAcceptsConsomme(t *testing.T) {
	host := Host{WindowsBuild: 26100, Runtime: wslver.MustParse("3.0.1"), TotalRAM: 32 << 30}
	for _, mode := range []string{"consomme", "Consomme", "virtioproxy"} {
		for _, f := range Lint(Parse("[wsl2]\nnetworkingMode="+mode+"\n"), table(t), host) {
			// Before 3.0.4 consomme also brings swiotlb=force, a real
			// warning; it must still never comment the line out.
			if strings.Contains(f.Message, "swiotlb=force") {
				if f.CommentOut {
					t.Errorf("%s: the swiotlb warning would comment out a working setting", mode)
				}
				continue
			}
			if f.Severity != Info {
				t.Errorf("%s: unexpected %s: %+v", mode, f.Severity, f)
			}
		}
	}
}

func TestCommentOut(t *testing.T) {
	cfg := Parse("[wsl2]\nbogus=1\nmemory=4GB\n")
	fs := Lint(cfg, table(t), Host{})
	lines := CommentOut(cfg, fs)
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "# wslkit:unknown key") || lines[2] != "# bogus=1" {
		t.Fatalf("lines = %q", lines)
	}
	if lines[3] != "memory=4GB" {
		t.Fatalf("untouched line changed: %q", lines[3])
	}
}

// sparseVhd was ignored from 2.5.6 to 3.0.1 and is honoured, as experimental,
// from 3.0.2 (microsoft/WSL#41726).
func TestLintSparseVhdByRuntime(t *testing.T) {
	cfg := Parse("[experimental]\nsparseVhd=true\n")
	for _, tc := range []struct {
		runtime string
		want    Severity
		frag    string
	}{
		{"2.5.5", "", ""},
		{"2.7.14", Warn, "ignored on WSL 2.7.14"},
		{"3.0.1", Warn, "ignored on WSL 3.0.1"},
		{"3.0.2", Info, "experimental"},
	} {
		fs := Lint(cfg, table(t), Host{Runtime: wslver.MustParse(tc.runtime)})
		f := find(fs, "experimental.sparseVhd", "sparse")
		if tc.want == "" {
			if f != nil {
				t.Errorf("%s: nothing to say before 2.5.6, got %+v", tc.runtime, f)
			}
			continue
		}
		if f == nil || f.Severity != tc.want || !strings.Contains(f.Message, tc.frag) {
			t.Errorf("%s: %+v, want %v containing %q", tc.runtime, f, tc.want, tc.frag)
		}
	}
	if f := find(Lint(Parse("[experimental]\nsparseVhd=false\n"), table(t), Host{Runtime: wslver.MustParse("3.0.1")}), "experimental.sparseVhd", "sparse"); f != nil {
		t.Errorf("false asks for nothing: %+v", f)
	}
}

// Before 3.0.4, virtiofs, consomme or an explicit swiotlb put swiotlb=force on
// the kernel command line (microsoft/WSL#41828).
func TestLintSwiotlbBefore304(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, runtime, key string
		warn                    bool
	}{
		{"virtiofs on 3.0.3", "[wsl2]\nvirtiofs=true\n", "3.0.3", "wsl2.virtiofs", true},
		{"consomme on 3.0.3", "[wsl2]\nnetworkingMode=consomme\n", "3.0.3", "wsl2.networkingMode", true},
		{"its old name too", "[wsl2]\nnetworkingMode=virtioproxy\n", "3.0.3", "wsl2.networkingMode", true},
		{"explicit pool on 3.0.3", "[experimental]\nswiotlb=128MB\n", "3.0.3", "experimental.swiotlb", true},
		{"fixed in 3.0.4", "[wsl2]\nvirtiofs=true\n", "3.0.4", "wsl2.virtiofs", false},
		{"virtio off means no pool", "[wsl2]\nvirtio=false\nvirtiofs=true\n", "3.0.3", "wsl2.virtiofs", false},
		{"virtiofs off", "[wsl2]\nvirtiofs=false\n", "3.0.3", "wsl2.virtiofs", false},
	} {
		fs := Lint(Parse(tc.cfg), table(t), Host{Runtime: wslver.MustParse(tc.runtime)})
		f := find(fs, tc.key, "swiotlb=force")
		if (f != nil) != tc.warn || (f != nil && f.Severity != Warn) {
			t.Errorf("%s: %+v, want warning %v", tc.name, f, tc.warn)
		}
	}
}
