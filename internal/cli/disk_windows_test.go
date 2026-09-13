//go:build windows

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/disk"
)

func newApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &App{Version: "test", Stdout: &out, Stderr: &errb}, &out, &errb
}

func TestDiskUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", []string{"disk"}, "wslkit disk:"},
		{"unknown subcommand", []string{"disk", "frobnicate"}, `unknown disk subcommand "frobnicate"`},
		{"list takes no arguments", []string{"disk", "list", "extra"}, "takes no arguments"},
		{"info needs a name", []string{"disk", "info"}, "needs the name of a distribution"},
		{"info takes one name", []string{"disk", "info", "One", "Two"}, "takes one distribution name"},
		{"info takes one name after flags", []string{"disk", "info", "--json", "One", "Two"}, "takes one distribution name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, errb := newApp()
			if code := a.Run(c.args); code != ExitUsage {
				t.Errorf("exit %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(errb.String(), c.want) {
				t.Errorf("stderr %q does not mention %q", errb.String(), c.want)
			}
		})
	}
}

func TestDiskHelpExitsZero(t *testing.T) {
	a, _, errb := newApp()
	if code := a.Run([]string{"disk", "help"}); code != ExitOK {
		t.Errorf("exit %d, want 0", code)
	}
	if !strings.Contains(errb.String(), "wslkit disk list") {
		t.Errorf("help does not list the subcommands: %q", errb.String())
	}
}

// A dry run must take no measurements at all. Being free of side effects means
// not starting a distribution to look inside it, not merely not writing.
func TestDiskDryRunReportsThatNothingWasAtStake(t *testing.T) {
	a, out, _ := newApp()
	if code := a.Run([]string{"disk", "list", "--dry-run"}); code != ExitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.Contains(out.String(), "--dry-run: this command only reads") {
		t.Errorf("got %q", out.String())
	}
}

func TestDiskDryRunInJSON(t *testing.T) {
	a, out, _ := newApp()
	if code := a.Run([]string{"disk", "info", "Whatever", "--dry-run", "--json"}); code != ExitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	var o map[string]any
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if o["dry_run"] != true || o["command"] != "info" {
		t.Errorf("unexpected object: %v", o)
	}
}

// The name may be written before or after the flags. Both are things people
// type, and the flag package stops at the first bare word, so both are handled
// explicitly.
func TestDiskInfoAcceptsTheNameOnEitherSideOfTheFlags(t *testing.T) {
	for _, args := range [][]string{
		{"disk", "info", "Nonexistent-Distro", "--json", "--dry-run"},
		{"disk", "info", "--json", "--dry-run", "Nonexistent-Distro"},
	} {
		a, out, errb := newApp()
		if code := a.Run(args); code != ExitOK {
			t.Fatalf("%v: exit %d, want 0 (stderr %q)", args, code, errb.String())
		}
		if !strings.Contains(out.String(), `"dry_run":true`) {
			t.Errorf("%v: got %q", args, out.String())
		}
	}
}

// The exit code is the contract a script branches on, so each failure that a
// caller can reasonably handle gets its own number.
func TestDiskExitCodeMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{disk.ErrNotFound, ExitDiskNotFound},
		{disk.ErrNoDistros, ExitDiskNotFound},
		{disk.ErrNoDefault, ExitDiskNotFound},
		{disk.ErrAmbiguous, ExitDiskNotFound},
		{disk.ErrRunning, ExitDiskBusy},
		{disk.ErrBusy, ExitDiskBusy},
		{disk.ErrNotWSL2, ExitDiskPreflight},
		{disk.ErrNotVHDX, ExitDiskPreflight},
		{disk.ErrRefused, ExitDiskPreflight},
		{errors.New("something else"), ExitFindings},
	} {
		if got := diskExitFor(c.err); got != c.want {
			t.Errorf("diskExitFor(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	// Wrapped errors must map the same way, since that is how they arrive.
	if got := diskExitFor(errors.Join(errors.New("context"), disk.ErrRunning)); got != ExitDiskBusy {
		t.Errorf("a wrapped ErrRunning mapped to %d, want %d", got, ExitDiskBusy)
	}
}

func TestDiskAppearsInTheTopLevelUsage(t *testing.T) {
	a, _, errb := newApp()
	a.Run(nil)
	if !strings.Contains(errb.String(), "wslkit disk ...") {
		t.Errorf("disk is missing from the top-level usage:\n%s", errb.String())
	}
}

// One question for the whole set, and end of input is a no: a piped command
// with nothing to answer with has not consented to anything.
func TestConfirm(t *testing.T) {
	for _, c := range []struct {
		name  string
		stdin string
		want  bool
	}{
		{"yes", "y\n", true},
		{"long yes", "YES\n", true},
		{"no", "n\n", false},
		{"bare enter", "\n", false},
		{"anything else", "maybe\n", false},
		{"end of input", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, _, _ := newApp()
			a.Stdin = strings.NewReader(c.stdin)
			if got := a.confirm("delete 1 file(s)?"); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// No stdin at all is also a no, not a crash.
func TestConfirmWithNoStdin(t *testing.T) {
	a, _, _ := newApp()
	if a.confirm("delete?") {
		t.Error("a missing stdin must not read as consent")
	}
}

// A lone --to used to fall through to a plain scan and exit 0, which reads as a
// repoint that happened and did not.
func TestOrphansRelinkAndToRequireEachOther(t *testing.T) {
	for _, args := range [][]string{
		{"disk", "orphans", "--to", `D:\a.vhdx`},
		{"disk", "orphans", "--relink", "Ubuntu"},
	} {
		a, _, errb := newApp()
		if code := a.Run(args); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(errb.String(), "go together") {
			t.Errorf("%v: stderr %q", args, errb.String())
		}
	}
}

func TestRelinkNeedsTwoArguments(t *testing.T) {
	for _, args := range [][]string{
		{"disk", "relink"},
		{"disk", "relink", "Ubuntu"},
		{"disk", "relink", "Ubuntu", `D:\a.vhdx`, "extra"},
	} {
		a, _, errb := newApp()
		if code := a.Run(args); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(errb.String(), "wslkit disk relink <distro> <path-to-vhdx>") {
			t.Errorf("%v: stderr %q", args, errb.String())
		}
	}
}

func TestOrphansTakesNoArguments(t *testing.T) {
	a, _, errb := newApp()
	if code := a.Run([]string{"disk", "orphans", "extra"}); code != ExitUsage {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "takes no arguments") {
		t.Errorf("stderr %q", errb.String())
	}
}
