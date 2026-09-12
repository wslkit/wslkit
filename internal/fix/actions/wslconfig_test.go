package actions

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
)

func TestWslConfigPlan(t *testing.T) {
	e := env.New("t")
	e.Host.OS = env.Ok(env.OSBuild{Build: 26100}, "t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	e.Config.WslConfigPath = `C:\Users\u\.wslconfig`
	e.Config.WslConfig = env.Ok("[wsl2]\r\nmemroy=4GB\r\nprocessors=2\r\n", "t")

	p, err := WslConfig{}.Plan(e, fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 || p.Steps[0].Kind != "file_copy" || p.Steps[1].Kind != "file_write" {
		t.Fatalf("steps = %+v", p.Steps)
	}
	content := p.Steps[1].Args[1]
	if !strings.Contains(content, "# wslkit:unknown key") || !strings.Contains(content, "\r\n# memroy=4GB") || !strings.Contains(content, "processors=2") {
		t.Fatalf("content = %q", content)
	}
	if !strings.HasSuffix(content, "\r\n") {
		t.Fatalf("trailing newline lost: %q", content)
	}
	if len(p.Rollback) != 1 || p.Rollback[0].Kind != "file_copy" || p.Rollback[0].Args[1] != e.Config.WslConfigPath {
		t.Fatalf("rollback = %+v", p.Rollback)
	}
	rec := &fix.Recording{}
	if err := fix.Apply(p, rec); err != nil || len(rec.Steps) != 3 {
		t.Fatalf("apply: %v %d", err, len(rec.Steps))
	}
}

func TestWslConfigPlanNothingToDo(t *testing.T) {
	e := env.New("t")
	e.Config.WslConfigPath = `C:\Users\u\.wslconfig`
	e.Config.WslConfig = env.Ok("[wsl2]\nmemory=4GB\n", "t")
	p, err := WslConfig{}.Plan(e, fix.Options{})
	if err != nil || len(p.Steps) != 1 || p.Steps[0].Kind != "note" || len(p.Rollback) != 0 {
		t.Fatalf("plan = %+v err=%v", p, err)
	}
	e.Config.WslConfig = env.Absent[string]("p")
	if _, err := (WslConfig{}).Plan(e, fix.Options{}); err == nil {
		t.Fatal("absent file must be an error")
	}
}
