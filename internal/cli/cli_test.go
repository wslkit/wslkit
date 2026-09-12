package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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
