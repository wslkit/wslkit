package data

import (
	"testing"

	"github.com/wslkit/wslkit/internal/wslerr"
)

func TestLoadErrors(t *testing.T) {
	e, err := LoadErrors()
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Contexts) < 30 {
		t.Fatalf("expected the generated context list, got %d entries (run tools/gen-errors)", len(e.Contexts))
	}
	if len(e.CodeNames) < 50 {
		t.Fatalf("expected the generated code_names list, got %d", len(e.CodeNames))
	}
	for _, name := range []string{"Wsl", "Service", "HCS", "CreateVm", "Plugin"} {
		if _, ok := e.Segments[name]; !ok {
			t.Errorf("segment %s missing", name)
		}
	}
}

func TestExplain(t *testing.T) {
	e, err := LoadErrors()
	if err != nil {
		t.Fatal(err)
	}
	p, err := wslerr.Parse("Error code: Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED")
	if err != nil {
		t.Fatal(err)
	}
	ex := e.Explain(p)
	if len(ex.Steps) != 5 || !ex.Steps[4].Known || ex.Steps[4].Desc == "" {
		t.Fatalf("steps = %+v", ex.Steps)
	}
	if ex.Code == nil || ex.Code.HRESULT != "0x80370102" || !ex.KnownToWSL {
		t.Fatalf("code = %+v known=%v", ex.Code, ex.KnownToWSL)
	}
	if len(ex.Probes) == 0 || ex.Probes[0] != "HST001" {
		t.Fatalf("probes = %v", ex.Probes)
	}

	// Bare HRESULT resolves by value.
	p, _ = wslerr.Parse("0x80370102")
	ex = e.Explain(p)
	if ex.CodeName != "HCS_E_HYPERV_NOT_INSTALLED" {
		t.Fatalf("hex lookup -> %q", ex.CodeName)
	}

	// The bucket has multiple causes.
	p, _ = wslerr.Parse("Wsl/Service/E_UNEXPECTED")
	ex = e.Explain(p)
	if ex.Code == nil || len(ex.Code.Causes) < 2 || ex.Probes[0] != "WSL001" {
		t.Fatalf("E_UNEXPECTED explanation = %+v", ex)
	}

	// Exit code.
	p, _ = wslerr.Parse("4294967295")
	ex = e.Explain(p)
	if ex.Code == nil || len(ex.Probes) == 0 {
		t.Fatalf("exit code explanation = %+v", ex)
	}

	// Unknown code still decodes the path.
	p, _ = wslerr.Parse("Wsl/Service/SOME_E_NEW")
	ex = e.Explain(p)
	if ex.Code != nil || len(ex.Steps) != 2 || len(ex.Probes) == 0 {
		t.Fatalf("unknown code explanation = %+v", ex)
	}
}

func TestValidateErrors(t *testing.T) {
	e := &Errors{Schema: "wslkit/errors/v1", Contexts: []ContextInfo{{"A", 1}, {"B", 0}}}
	if err := e.Validate(); err == nil {
		t.Fatal("unsorted contexts must fail")
	}
	e = &Errors{Schema: "wslkit/errors/v1", Contexts: []ContextInfo{{"A", 0}}, Segments: map[string]Segment{"Z": {Desc: "x"}}}
	if err := e.Validate(); err == nil {
		t.Fatal("segment for unknown context must fail")
	}
}
