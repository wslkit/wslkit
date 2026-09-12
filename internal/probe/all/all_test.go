package all

import "testing"

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
