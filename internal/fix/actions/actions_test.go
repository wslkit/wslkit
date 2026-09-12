package actions

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
)

func TestUpdatePlanRecords(t *testing.T) {
	e := env.New("t")
	e.Runtime.Version = env.Ok("2.4.13.0", "test")
	p, err := Update{}.Plan(e, fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &fix.Recording{}
	if err := fix.Apply(p, rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Steps) != 1 || rec.Steps[0].Kind != "exec" || rec.Steps[0].Args[1] != "--update" {
		t.Fatalf("steps = %+v", rec.Steps)
	}
	if len(p.Rollback) == 0 || !strings.Contains(p.Rollback[0].Description, "2.4.13") {
		t.Fatalf("rollback should name the previous version: %+v", p.Rollback)
	}
	if !strings.Contains(fix.Describe(p), "wsl.exe --update") {
		t.Fatal("describe should show the command")
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("UPDATE"); !ok {
		t.Fatal("case-insensitive lookup")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown id should fail")
	}
}
