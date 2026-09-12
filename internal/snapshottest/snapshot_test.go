// Package snapshottest runs every probe against each saved environment under
// testdata/snapshots and checks the ranked findings. This is the primary test
// suite for probes (ADR 0004). It runs on any OS.
package snapshottest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/probe/all"
	"github.com/wslkit/wslkit/internal/render"
)

type expectation struct {
	// Top is an ordered prefix of the ranked results that must match exactly by ID and status.
	Top []finding `json:"top"`
	// Require lists findings that must appear anywhere with the given status.
	Require []finding `json:"require,omitempty"`
	// NoFail asserts that no result has status FAIL.
	NoFail bool `json:"no_fail,omitempty"`
	// MaxFail, when set, caps the number of FAIL results (a broken machine
	// should get one root cause, not a pile-up).
	MaxFail *int `json:"max_fail,omitempty"`
}

type finding struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
	FixID         string  `json:"fix_id,omitempty"`
}

var (
	reUserPath = regexp.MustCompile(`(?i)Users\\\\([^\\"]+)`)
	reSIDLeak  = regexp.MustCompile(`S-1-5-21-\d`)
)

func TestSnapshots(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "snapshots")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("no snapshot corpus at %s: %v", root, err)
	}
	if len(dirs) == 0 {
		t.Fatal("snapshot corpus is empty")
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		t.Run(d.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "env.json"))
			if err != nil {
				t.Fatal(err)
			}
			checkRedacted(t, string(raw))
			e, err := render.LoadSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			results := probe.RunAll(all.Probes(), e)

			expRaw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
			if err != nil {
				t.Fatal("every snapshot needs expected.json: ", err)
			}
			var exp expectation
			if err := json.Unmarshal(expRaw, &exp); err != nil {
				t.Fatal(err)
			}
			if len(exp.Top) > len(results) {
				t.Fatalf("expected %d top findings, only %d results", len(exp.Top), len(results))
			}
			for i, want := range exp.Top {
				got := results[i]
				if got.ID != want.ID || string(got.Status) != want.Status {
					t.Errorf("rank %d: got %s %s (%q), want %s %s\n%s", i+1, got.ID, got.Status, got.Summary, want.ID, want.Status, dump(results))
					continue
				}
				checkFinding(t, got, want)
			}
			for _, want := range exp.Require {
				found := false
				for _, got := range results {
					if got.ID == want.ID {
						found = true
						if string(got.Status) != want.Status {
							t.Errorf("%s: status %s, want %s (%q)", got.ID, got.Status, want.Status, got.Summary)
						}
						checkFinding(t, got, want)
					}
				}
				if !found {
					t.Errorf("required finding %s not present", want.ID)
				}
			}
			fails := 0
			for _, r := range results {
				if r.Status == probe.Fail {
					fails++
					if exp.NoFail {
						t.Errorf("unexpected FAIL: %s %q", r.ID, r.Summary)
					}
				}
			}
			if exp.MaxFail != nil && fails > *exp.MaxFail {
				t.Errorf("%d FAIL results, at most %d allowed\n%s", fails, *exp.MaxFail, dump(results))
			}
			// Contract: every non-OK finding ends in an action.
			for _, r := range results {
				if (r.Status == probe.Fail || r.Status == probe.Warn || r.Status == probe.Unknown) && r.FixHint == "" {
					t.Errorf("%s %s has no FixHint: %q", r.ID, r.Status, r.Summary)
				}
			}
		})
	}
}

func checkFinding(t *testing.T, got probe.Result, want finding) {
	if want.MinConfidence > 0 && got.Confidence < want.MinConfidence {
		t.Errorf("%s: confidence %.2f below %.2f", got.ID, got.Confidence, want.MinConfidence)
	}
	if want.FixID != "" && got.FixID != want.FixID {
		t.Errorf("%s: fix_id %q, want %q", got.ID, got.FixID, want.FixID)
	}
}

func checkRedacted(t *testing.T, s string) {
	// The regexp package has no lookahead; emulate it.
	for _, m := range reUserPath.FindAllStringSubmatch(s, -1) {
		if m[1] != "<user>" {
			t.Errorf("snapshot leaks a profile name: %q", m[0])
		}
	}
	if reSIDLeak.MatchString(s) {
		t.Errorf("snapshot leaks a machine SID")
	}
	if strings.Contains(s, `"hostname": "`) && !strings.Contains(s, `"hostname": "<host>"`) {
		t.Errorf("snapshot leaks the hostname")
	}
}

func dump(results []probe.Result) string {
	var sb strings.Builder
	for _, r := range results {
		sb.WriteString("  " + string(r.Status) + " " + r.ID + " " + r.Summary + "\n")
	}
	return sb.String()
}
