//go:build windows

package cli

import (
	"os"
	"strings"
	"testing"
)

func TestRefuseScheduledTaskExplainsAndStops(t *testing.T) {
	a, _, errb := newApp()
	if !a.refuseScheduledTask("wslkit guard install", false) {
		t.Fatal("without --force it must stop")
	}
	for _, want := range []string{"was not run", "Defender", "--force", "Protection history", "issues/19"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("the refusal should mention %q: %s", want, errb.String())
		}
	}
	a, _, errb = newApp()
	if a.refuseScheduledTask("wslkit guard install", true) || errb.Len() != 0 {
		t.Errorf("with --force it must go ahead quietly: %q", errb.String())
	}
}

// Running either command in a test would register a real task, and get the
// test binary quarantined, so the wiring is checked in the source: each
// refuses before the call that registers anything.
func TestTaskInstallersRefuseBeforeRegistering(t *testing.T) {
	for file, c := range map[string]struct{ guard, register string }{
		"guard_windows.go":          {`a.refuseScheduledTask("wslkit guard install", *force)`, "guard.Install("},
		"disk_automount_windows.go": {`a.refuseScheduledTask("wslkit disk automount install", *force)`, "schtask.Register("},
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		g, r := strings.Index(src, c.guard), strings.Index(src, c.register)
		if g < 0 {
			t.Errorf("%s does not refuse without --force", file)
			continue
		}
		if r < 0 || g > r {
			t.Errorf("%s registers the task before refusing", file)
		}
	}
}
