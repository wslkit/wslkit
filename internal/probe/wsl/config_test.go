package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func envWithConfig(text string) *env.Env {
	e := env.New("t")
	e.Host.OS = env.Ok(env.OSBuild{Major: 10, Build: 19045}, "t")
	e.Host.TotalMemoryBytes = env.Ok(uint64(16<<30), "t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	e.Config.WslConfigPath = `C:\Users\u\.wslconfig`
	e.Config.WslConfig = env.Ok(text, "t")
	e.Config.PathsExist = map[string]bool{`c:\nope\kernel`: false}
	return e
}

func TestConfigLintAbsentAndClean(t *testing.T) {
	e := env.New("t")
	e.Config.WslConfig = env.Absent[string]("p")
	if r := (ConfigLint{}).Run(e); r.Status != probe.OK {
		t.Fatalf("absent -> %s", r.Status)
	}
	r := (ConfigLint{}).Run(envWithConfig("[wsl2]\nmemory=4GB\n"))
	if r.Status != probe.OK {
		t.Fatalf("clean -> %s %q", r.Status, r.Detail)
	}
}

func TestConfigLintWarnAndFail(t *testing.T) {
	r := (ConfigLint{}).Run(envWithConfig("[wsl2]\nmemroy=4GB\n"))
	if r.Status != probe.Warn || r.FixID != "wslconfig" || !strings.Contains(r.Detail, "unknown key") {
		t.Fatalf("typo -> %s %q %q", r.Status, r.FixID, r.Detail)
	}
	r = (ConfigLint{}).Run(envWithConfig("[wsl2]\nkernel=C:\\\\nope\\\\kernel\n"))
	if r.Status != probe.Fail || !strings.Contains(r.Detail, "cannot start") {
		t.Fatalf("missing kernel -> %s %q", r.Status, r.Detail)
	}
}
