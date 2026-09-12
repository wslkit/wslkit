package all

import (
	"testing"

	"github.com/wslkit/wsldoctor/internal/data"
)

// Dependencies must be declared before their dependents and IDs must be unique.
func TestOrderingAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Probes() {
		if seen[p.ID()] {
			t.Fatalf("duplicate probe ID %s", p.ID())
		}
		for _, dep := range p.Needs() {
			if !seen[dep] {
				t.Fatalf("%s needs %s, which is not registered before it", p.ID(), dep)
			}
		}
		if p.Milestone() == "" || p.Title() == "" {
			t.Fatalf("%s lacks milestone or title", p.ID())
		}
		seen[p.ID()] = true
	}
}

// Every probe the error dictionary points at must exist, so `explain` never
// promises a probe that does not run. Planned probes belong in notes, not in
// the probes lists.
func TestErrorDictionaryReferencesRegisteredProbes(t *testing.T) {
	e, err := data.LoadErrors()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range Probes() {
		ids[p.ID()] = true
	}
	for _, id := range e.AllProbeIDs() {
		if !ids[id] {
			t.Errorf("errors.json references unknown probe %s", id)
		}
	}
}

// ByID returns the registered probes with the given IDs, plus their Needs
// closure, in registration order.
func TestByIDIncludesDependencies(t *testing.T) {
	sel := ByID([]string{"WSL001"})
	var ids []string
	for _, p := range sel {
		ids = append(ids, p.ID())
	}
	if len(ids) != 2 || ids[0] != "WSL002" || ids[1] != "WSL001" {
		t.Fatalf("ByID(WSL001) = %v, want [WSL002 WSL001]", ids)
	}
}
