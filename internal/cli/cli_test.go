package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/wslkit/internal/fix"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", "snapshots", name, "env.json")
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := &App{Version: "test", Stdout: &out, Stderr: &errb}
	code := app.Run(args)
	return code, out.String(), errb.String()
}

func TestDoctorCheckFromSnapshot(t *testing.T) {
	code, out, _ := run(t, "doctor", "check", "--from-snapshot", fixture("ubuntu-2604-on-2.4.13"))
	if code != ExitFindings {
		t.Fatalf("exit %d, want %d", code, ExitFindings)
	}
	if !strings.Contains(out, "FAIL    WSL001") || !strings.HasPrefix(out, "wslkit test") {
		t.Fatalf("expected WSL001 FAIL first under a wslkit header:\n%s", out)
	}
}

func TestDoctorDefaultsToCheck(t *testing.T) {
	code, out, _ := run(t, "doctor", "--from-snapshot", fixture("win10-2.7.13-healthy"))
	if code != ExitOK || !strings.Contains(out, "OK      WSL002") {
		t.Fatalf("bare doctor should run check: exit %d\n%s", code, out)
	}
}

func TestExplainRunsMappedProbes(t *testing.T) {
	code, out, _ := run(t, "doctor", "explain", "--from-snapshot", fixture("ubuntu-2604-on-2.4.13"), "Catastrophic failure Error code: Wsl/Service/E_UNEXPECTED")
	if code != ExitFindings {
		t.Fatalf("exit %d, want %d\n%s", code, ExitFindings, out)
	}
	for _, want := range []string{"Error: Wsl/Service/E_UNEXPECTED", "wslservice.exe", "Known causes:", "cgroup", "FAIL    WSL001"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "EVT001") {
		t.Errorf("unrelated probe ran:\n%s", out)
	}
}

func TestExplainHexAndUsage(t *testing.T) {
	code, out, _ := run(t, "doctor", "explain", "--from-snapshot", fixture("win10-2.7.13-healthy"), "0x80370102")
	if code != ExitOK || !strings.Contains(out, "HCS_E_HYPERV_NOT_INSTALLED") || !strings.Contains(out, "OK      HST003") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, _, errb := run(t, "doctor", "explain", "--from-snapshot", fixture("win10-2.7.13-healthy"), "nothing useful here")
	if code != ExitUsage || !strings.Contains(errb, "no WSL error code") {
		t.Fatalf("exit %d stderr %q", code, errb)
	}
	if code, _, _ = run(t, "doctor", "explain"); code != ExitUsage {
		t.Fatalf("no args should be usage error, got %d", code)
	}
}

func TestExplainJSON(t *testing.T) {
	code, out, _ := run(t, "doctor", "explain", "--json", "--from-snapshot", fixture("win10-2.7.13-healthy"), "Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED")
	if code != ExitOK || !strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, `"HST001"`) || !strings.Contains(out, `"wslkit/result/v1"`) {
		t.Fatalf("exit %d\n%s", code, out[:min(len(out), 400)])
	}
}

func TestTopLevelRouting(t *testing.T) {
	if code, out, _ := run(t, "version"); code != ExitOK || !strings.HasPrefix(out, "wslkit test") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, errb := run(t, "checkup"); code != ExitUsage || !strings.Contains(errb, "unknown command") {
		t.Fatalf("unknown command: %d %q", code, errb)
	}
	if code, _, errb := run(t, "doctor", "bogus"); code != ExitUsage || !strings.Contains(errb, "unknown doctor subcommand") {
		t.Fatalf("unknown doctor sub: %d %q", code, errb)
	}
	if code, _, _ := run(t); code != ExitUsage {
		t.Fatalf("no args: %d", code)
	}
}

// undo is the one command whose whole purpose is to change the machine back,
// and an earlier version took the id from args[0] and ignored everything after
// it. `doctor undo <id> --dry-run` therefore performed the rollback: an
// instruction to change nothing, obeyed by changing something. This is that
// bug, kept.
func TestUndoDryRunDoesNotRollBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)

	j := fix.Journal{Dir: fix.DefaultJournalDir()}
	id, err := j.Save(fix.Plan{
		FixID:     "test",
		Title:     "a fix that would do something",
		CreatedAt: time.Now(),
		Steps:     []fix.Step{{Kind: "note", Description: "did a thing"}},
		Rollback:  []fix.Step{{Kind: "exec", Args: []string{"cmd.exe", "/c", "echo undone"}, Description: "undo the thing"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	app := &App{Version: "test", Stdout: &out, Stderr: &errb}
	// No Stdin: a run with nothing to answer a prompt with must not roll back
	// either, which is the other half of the same rule.
	if code := app.Run([]string{"doctor", "undo", id, "--dry-run"}); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "nothing was rolled back") {
		t.Errorf("a dry run must say it changed nothing: %q", out.String())
	}
	// fix/exec panics under WSLKIT_TEST rather than running anything, so
	// reaching the executor at all would fail the test loudly. Belt and
	// braces: the rollback step's text must not appear as something done.
	if strings.Contains(out.String(), "Rolling back") {
		t.Errorf("a dry run must not roll back: %q", out.String())
	}
}

// Without --yes and with no way to answer, undo declines rather than proceeding.
func TestUndoWithoutConfirmationDeclines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	j := fix.Journal{Dir: fix.DefaultJournalDir()}
	id, err := j.Save(fix.Plan{
		FixID: "test", Title: "t", CreatedAt: time.Now(),
		Rollback: []fix.Step{{Kind: "exec", Args: []string{"cmd.exe"}, Description: "undo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	app := &App{Version: "test", Stdout: &out, Stderr: &errb}
	if code := app.Run([]string{"doctor", "undo", id}); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "nothing was rolled back") {
		t.Errorf("out = %q", out.String())
	}
}

// A second positional is a typo, not a second journal entry.
func TestUndoRejectsTwoIds(t *testing.T) {
	var out, errb bytes.Buffer
	app := &App{Version: "test", Stdout: &out, Stderr: &errb}
	if code := app.Run([]string{"doctor", "undo", "one", "two"}); code != ExitUsage {
		t.Fatalf("exit %d, want a usage error", code)
	}
}
