package disk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCanonicalPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`C:\wsl\Ubuntu\ext4.vhdx`, `c:\wsl\ubuntu\ext4.vhdx`},
		{`\\?\C:\wsl\Ubuntu\ext4.vhdx`, `c:\wsl\ubuntu\ext4.vhdx`},
		{`C:/wsl/Ubuntu/ext4.vhdx`, `c:\wsl\ubuntu\ext4.vhdx`},
		{`C:\wsl\Ubuntu\`, `c:\wsl\ubuntu`},
		{`C:\`, `c:\`},
		{`\\?\UNC\server\share\d.vhdx`, `\\server\share\d.vhdx`},
	} {
		if got := CanonicalPath(c.in); got != c.want {
			t.Errorf("CanonicalPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The UNC form begins with the plain prefix, so stripping only the shorter one
// leaves UNC\server\share behind and every comparison against it fails.
func TestCanonicalPathHandlesUNCBeforeThePlainPrefix(t *testing.T) {
	if got := CanonicalPath(`\\?\UNC\srv\s\a.vhdx`); strings.Contains(got, "unc\\") {
		t.Errorf("the UNC prefix was not rewritten: %q", got)
	}
}

func TestSamePath(t *testing.T) {
	if !SamePath(`\\?\C:\wsl\a.vhdx`, `c:/WSL/a.vhdx`) {
		t.Error("two spellings of the same file should compare equal")
	}
	if SamePath(`C:\wsl\a.vhdx`, `C:\wsl\b.vhdx`) {
		t.Error("different files must not compare equal")
	}
}

func TestDirAndBase(t *testing.T) {
	for _, c := range []struct{ in, dir, base string }{
		{`C:\wsl\Ubuntu\ext4.vhdx`, `C:\wsl\Ubuntu`, `ext4.vhdx`},
		{`C:\a.vhdx`, `C:\`, `a.vhdx`},
		{`ext4.vhdx`, ``, `ext4.vhdx`},
	} {
		if got := DirOf(c.in); got != c.dir {
			t.Errorf("DirOf(%q) = %q, want %q", c.in, got, c.dir)
		}
		if got := BaseOf(c.in); got != c.base {
			t.Errorf("BaseOf(%q) = %q, want %q", c.in, got, c.base)
		}
	}
}

// Some registrations carry the extended-length prefix and some do not, on the
// same machine. Rewriting a value must keep whichever form it had.
func TestWithSamePrefixAsPreservesTheStoredForm(t *testing.T) {
	if got := WithSamePrefixAs(`\\?\C:\old`, `D:\new`); got != `\\?\D:\new` {
		t.Errorf("got %q", got)
	}
	if got := WithSamePrefixAs(`C:\old`, `D:\new`); got != `D:\new` {
		t.Errorf("got %q", got)
	}
	if got := WithSamePrefixAs(`C:\old`, `\\?\D:\new`); got != `D:\new` {
		t.Errorf("a prefix should be dropped to match the stored form: %q", got)
	}
}

// ---------------------------------------------------------------- relink

func relinkEnv(target string, hadName bool) (Env, *fakeRegistry, *fakeFS, *fakeHost) {
	r := reg("Ubuntu")
	fsys := &fakeFS{files: map[string]fakeFile{target: {size: 1, onDisk: 1}}}
	registry := &fakeRegistry{
		list:   []Registration{r},
		values: map[string]string{"{Ubuntu}\x00BasePath": r.BasePath},
	}
	if hadName {
		registry.values["{Ubuntu}\x00VhdFileName"] = "ext4.vhdx"
	}
	host := &fakeHost{}
	return Env{Registry: registry, FS: fsys, Disks: &fakeDisks{}, Host: host, Clock: &fakeClock{}}, registry, fsys, host
}

func TestRelinkWritesTheRegistrationAndChecksItBoots(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, registry, _, host := relinkEnv(target, true)

	res, err := Relink(context.Background(), e, reg("Ubuntu"), target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.BasePath != `D:\wsl\Ubuntu` {
		t.Errorf("base path = %q", res.BasePath)
	}
	if got := registry.values["{Ubuntu}\x00BasePath"]; got != `D:\wsl\Ubuntu` {
		t.Errorf("BasePath was not written: %q", got)
	}
	// The boot check must have run, or nothing was verified.
	if !strings.Contains(strings.Join(host.calls, "; "), "/bin/sh -c :") {
		t.Errorf("expected a boot check, got %v", host.calls)
	}
}

// A registration that never had a VhdFileName relies on the default. Adding the
// value would change its layout rather than repair its path.
func TestRelinkDoesNotAddAVhdFileNameThatWasNeverThere(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, registry, _, _ := relinkEnv(target, false)
	if _, err := Relink(context.Background(), e, reg("Ubuntu"), target, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.values["{Ubuntu}\x00VhdFileName"]; ok {
		t.Error("VhdFileName should not have been created")
	}
}

// But it cannot be pointed at a differently named disk either, because the
// default is what it will open.
func TestRelinkRefusesADifferentNameWithNoVhdFileName(t *testing.T) {
	target := `D:\wsl\Ubuntu\other.vhdx`
	e, _, _, _ := relinkEnv(target, false)
	_, err := Relink(context.Background(), e, reg("Ubuntu"), target, nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "ext4.vhdx") {
		t.Errorf("the refusal should name the default: %v", err)
	}
}

// If the distribution will not boot from the new path, the registry goes back.
// Leaving it repointed at a disk that does not work is worse than not trying.
func TestRelinkRollsBackWhenTheDistroWillNotStart(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, registry, _, _ := relinkEnv(target, true)
	e.Host = &scriptedHost{responses: []CommandResult{{ExitCode: 1, Stderr: "no such file"}}}

	_, err := Relink(context.Background(), e, reg("Ubuntu"), target, nil)
	if !errors.Is(err, ErrSmokeTest) {
		t.Fatalf("want ErrSmokeTest, got %v", err)
	}
	if got := registry.values["{Ubuntu}\x00BasePath"]; got != `C:\wsl\Ubuntu` {
		t.Errorf("BasePath was not restored: %q", got)
	}
	if got := registry.values["{Ubuntu}\x00VhdFileName"]; got != "ext4.vhdx" {
		t.Errorf("VhdFileName was not restored: %q", got)
	}
	if !strings.Contains(err.Error(), "put back") {
		t.Errorf("the user should be told the registry was restored: %v", err)
	}
}

// A write that fails halfway must not leave the registration half-rewritten.
func TestRelinkRollsBackAPartialWrite(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, registry, _, _ := relinkEnv(target, true)
	registry.failWriteOn = "VhdFileName"

	if _, err := Relink(context.Background(), e, reg("Ubuntu"), target, nil); err == nil {
		t.Fatal("expected the write to fail")
	}
	if got := registry.values["{Ubuntu}\x00BasePath"]; got != `C:\wsl\Ubuntu` {
		t.Errorf("the first write was not rolled back: %q", got)
	}
}

func TestPlanRelinkRefusals(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, _, _, _ := relinkEnv(target, true)

	if _, err := PlanRelink(e, reg("Legacy", func(r *Registration) { r.Version = 1 }), target, false, true); !errors.Is(err, ErrNotWSL2) {
		t.Errorf("WSL 1: %v", err)
	}
	if _, err := PlanRelink(e, reg("Ubuntu"), `D:\nowhere.vhdx`, false, true); err == nil {
		t.Error("a target that is not there should be refused")
	}
	if _, err := PlanRelink(e, reg("Ubuntu"), `C:\wsl\Ubuntu\ext4.vhdx`, false, true); err == nil {
		t.Error("pointing at where it already points should be refused")
	}
	if _, err := PlanRelink(e, reg("Ubuntu"), target, true, true); !errors.Is(err, ErrRunning) {
		t.Errorf("a running distribution should be refused: %v", err)
	}
}

// If the distribution is in fact running, the boot check executes in the
// already-booted guest, which booted from the old disk, and passes without
// testing anything. Not knowing is therefore a refusal, not a shrug.
func TestPlanRelinkRefusesWhenItCannotTellWhetherTheDistroIsRunning(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, _, _, _ := relinkEnv(target, true)
	_, err := PlanRelink(e, reg("Ubuntu"), target, false, false)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "cannot be verified") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

func TestPlanRelinkMarksTheRegistryWriteUndoable(t *testing.T) {
	target := `D:\wsl\Ubuntu\ext4.vhdx`
	e, _, _, _ := relinkEnv(target, true)
	p, err := PlanRelink(e, reg("Ubuntu"), target, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 2 || !p.Steps[0].Undoable {
		t.Fatalf("the registry write should be undoable: %+v", p.Steps)
	}
	if p.Steps[1].Undoable || p.Steps[1].Irreversible {
		t.Errorf("the boot check changes nothing: %+v", p.Steps[1])
	}
	if err := p.Valid(); err != nil {
		t.Errorf("plan should be valid: %v", err)
	}
}

// Rollbacks run newest first, and one that fails must not stop the rest:
// stopping would leave more of the change in place, not less.
func TestUndoStackUnwindsInReverseAndKeepsGoing(t *testing.T) {
	var order []string
	var s UndoStack
	s.Push("first", func() error { order = append(order, "first"); return nil })
	s.Push("second", func() error { order = append(order, "second"); return errors.New("boom") })
	s.Push("third", func() error { order = append(order, "third"); return nil })

	err := s.Unwind()
	if err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("the failure should be reported: %v", err)
	}
	if strings.Join(order, ",") != "third,second,first" {
		t.Errorf("wrong order: %v", order)
	}
	if s.Len() != 0 {
		t.Error("the stack should be empty afterwards")
	}
}
