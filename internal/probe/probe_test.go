package probe

import (
	"testing"

	"github.com/wslkit/wslkit/internal/env"
)

type fake struct {
	Base
	res Result
}

func (f fake) Run(*env.Env) Result { r := f.res; r.ID = f.PID; return r }

type panicky struct{ Base }

func (panicky) Run(*env.Env) Result { panic("boom") }

func TestRunAllCascadeAndRank(t *testing.T) {
	probes := []Probe{
		fake{Base{PID: "A", PMilestone: "M1"}, Result{Status: Fail, Confidence: 0.9}},
		fake{Base{PID: "B", PMilestone: "M1", PNeeds: []string{"A"}}, Result{Status: OK, Confidence: 1}},
		fake{Base{PID: "C", PMilestone: "M1"}, Result{Status: Warn, Confidence: 0.5}},
		fake{Base{PID: "D", PMilestone: "M1"}, Result{Status: Fail, Confidence: 0.95}},
		panicky{Base{PID: "E", PMilestone: "M2"}},
	}
	rs := RunAll(probes, env.New("t"))
	want := []string{"D", "A", "C", "E", "B"}
	for i, id := range want {
		if rs[i].ID != id {
			t.Fatalf("rank[%d] = %s (%s), want %s", i, rs[i].ID, rs[i].Status, id)
		}
	}
	if rs[4].Status != Skipped {
		t.Errorf("B should be skipped, got %s", rs[4].Status)
	}
	if rs[3].Status != Unknown {
		t.Errorf("panicking probe should be UNKNOWN, got %s", rs[3].Status)
	}
	if ExitCode(rs) != 1 {
		t.Error("exit code should be 1 with a FAIL")
	}
	if got := Filter(probes, map[string]bool{"M2": true}); len(got) != 1 {
		t.Errorf("filter M2 = %d probes", len(got))
	}
}
