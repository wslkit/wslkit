//go:build windows

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/top"
)

// `wslkit top help` used to take "help" for a distribution name, measure
// nothing, and say that no distribution was running. It prints the page, which
// carries the reading guide that used to follow every report.
func TestTopHelpPrintsThePage(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var out, errb bytes.Buffer
		a := &App{Stdout: &out, Stderr: &errb}
		if code := a.top([]string{arg}); code != ExitOK {
			t.Errorf("%s: exit %d", arg, code)
		}
		page := errb.String()
		for _, want := range []string{"wslkit top", "--wslc", "--once", "reading the output", top.ReadingGuide} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: the page lacks %q", arg, want[:min(len(want), 40)])
			}
		}
		if strings.Contains(out.String(), "utility VM") {
			t.Errorf("%s: measured instead of printing help:\n%s", arg, out.String())
		}
	}
}
