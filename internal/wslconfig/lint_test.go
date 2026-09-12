package wslconfig

import (
	"strings"
	"testing"

	"github.com/wslkit/wsldoctor/internal/data"
	"github.com/wslkit/wsldoctor/internal/wslver"
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
	host := Host{WindowsBuild: 26100, Runtime: wslver.MustParse("2.7.13"), TotalRAM: 32 << 30}
	for _, f := range Lint(cfg, table(t), host) {
		if f.Severity != Info {
			t.Errorf("unexpected %s: %+v", f.Severity, f)
		}
	}
}

func TestCommentOut(t *testing.T) {
	cfg := Parse("[wsl2]\nbogus=1\nmemory=4GB\n")
	fs := Lint(cfg, table(t), Host{})
	lines := CommentOut(cfg, fs)
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "# wsldoctor: unknown key") || lines[2] != "# bogus=1" {
		t.Fatalf("lines = %q", lines)
	}
	if lines[3] != "memory=4GB" {
		t.Fatalf("untouched line changed: %q", lines[3])
	}
}
