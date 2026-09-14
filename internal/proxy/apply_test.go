package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeDistro stands in for a distribution: a map of paths to contents, and the
// two shell scripts apply actually runs against it.
type fakeDistro struct {
	files map[string]string
	// calls records what was run, so a test can assert that nothing was
	// written when nothing should have been.
	calls []string
	// failWrite makes a write to this path fail.
	failWrite string
}

func (f *fakeDistro) Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error) {
	f.calls = append(f.calls, script)

	switch {
	// The read script: base64 the file, or print the absent marker.
	case strings.HasPrefix(script, "if [ -f "):
		path := unquoteFirst(script)
		body, ok := f.files[path]
		if !ok {
			return absentMarker + "\n", nil
		}
		return base64.StdEncoding.EncodeToString([]byte(body)) + "\n", nil

	case strings.HasPrefix(script, "rm -f "):
		delete(f.files, unquoteFirst(script))
		return "", nil

	// The write script. Parsed rather than pattern-matched, so the test
	// exercises the same heredoc the distribution would.
	case strings.HasPrefix(script, "set -e\n"):
		path := strings.TrimSuffix(unquoteAt(script, 1), ".wslkit-tmp")
		if path == f.failWrite {
			return "mv: cannot move", fmt.Errorf("exit status 1")
		}
		body, err := heredocBody(script)
		if err != nil {
			return "", err
		}
		if f.files == nil {
			f.files = map[string]string{}
		}
		f.files[path] = body
		return "", nil
	}
	return "", fmt.Errorf("unexpected script: %s", script)
}

// heredocBody decodes the content the write script carries.
func heredocBody(script string) (string, error) {
	_, rest, ok := strings.Cut(script, "<<'"+heredocTag+"'\n")
	if !ok {
		return "", fmt.Errorf("no heredoc in script: %s", script)
	}
	enc, _, ok := strings.Cut(rest, "\n"+heredocTag+"\n")
	if !ok {
		return "", fmt.Errorf("unterminated heredoc: %s", script)
	}
	b, err := base64.StdEncoding.DecodeString(stripSpace(enc))
	return string(b), err
}

// unquoteFirst returns the first single-quoted string in a script.
func unquoteFirst(s string) string { return unquoteAt(s, 0) }

// unquoteAt returns the nth single-quoted string in a script.
func unquoteAt(s string, n int) string {
	var found []string
	for {
		i := strings.Index(s, "'")
		if i < 0 {
			break
		}
		rest := s[i+1:]
		j := strings.Index(rest, "'")
		if j < 0 {
			break
		}
		found = append(found, rest[:j])
		s = rest[j+1:]
	}
	if n < len(found) {
		return found[n]
	}
	return ""
}

func testSettings() Settings {
	return Settings{
		HTTP:    "http://172.20.240.1:3128",
		HTTPS:   "http://172.20.240.1:3128",
		NoProxy: []string{"localhost", "127.0.0.1", ".corp.example"},
		Source:  "test",
	}
}

func apply(t *testing.T, d *fakeDistro, s Settings) {
	t.Helper()
	changes, err := PlanApply(context.Background(), d, "Ubuntu", s, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), d, "Ubuntu", changes, time.Second); err != nil {
		t.Fatal(err)
	}
}

// The five places, because each is read by something that reads none of the
// others.
func TestApplyWritesEveryPlaceThatMatters(t *testing.T) {
	d := &fakeDistro{files: map[string]string{}}
	apply(t, d, testSettings())

	for _, path := range []string{
		EnvFile,
		"/etc/environment",
		"/etc/profile.d/99-wslkit-proxy.sh",
		"/etc/apt/apt.conf.d/99wslkit-proxy",
		"/etc/systemd/system.conf.d/wslkit-proxy.conf",
	} {
		body, ok := d.files[path]
		if !ok {
			t.Errorf("%s was not written", path)
			continue
		}
		if !strings.Contains(body, "172.20.240.1:3128") {
			t.Errorf("%s does not carry the proxy:\n%s", path, body)
		}
	}
	// The systemd drop-in is the one that decides whether a unit is
	// proxied, and it needs its own directive rather than plain KEY=value.
	if unit := d.files["/etc/systemd/system.conf.d/wslkit-proxy.conf"]; !strings.Contains(unit, "DefaultEnvironment=") {
		t.Errorf("the systemd drop-in needs DefaultEnvironment:\n%s", unit)
	}
	// apt has its own language and does not read the environment from a
	// timer.
	if apt := d.files["/etc/apt/apt.conf.d/99wslkit-proxy"]; !strings.Contains(apt, `Acquire::https::Proxy "http://172.20.240.1:3128";`) {
		t.Errorf("apt configuration is wrong:\n%s", apt)
	}
}

// A user's own /etc/environment is not this tool's to rewrite.
func TestApplyKeepsWhatWasAlreadyInEnvironment(t *testing.T) {
	d := &fakeDistro{files: map[string]string{
		"/etc/environment": "PATH=/usr/local/bin:/usr/bin\nEDITOR=vim\n",
	}}
	apply(t, d, testSettings())

	got := d.files["/etc/environment"]
	for _, want := range []string{"PATH=/usr/local/bin:/usr/bin", "EDITOR=vim", BeginMarker, "http_proxy=", EndMarker} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Applying twice must not leave two blocks: a file with six copies of the same
// variable is one where nobody can tell which is live.
func TestApplyTwiceIsIdempotent(t *testing.T) {
	d := &fakeDistro{files: map[string]string{"/etc/environment": "EDITOR=vim\n"}}
	apply(t, d, testSettings())
	first := d.files["/etc/environment"]
	apply(t, d, testSettings())
	second := d.files["/etc/environment"]

	if first != second {
		t.Errorf("a second apply changed the file:\n%q\n%q", first, second)
	}
	if n := strings.Count(second, BeginMarker); n != 1 {
		t.Errorf("%d managed blocks, want 1:\n%s", n, second)
	}
}

// Nothing to do must do nothing, including writing files with identical
// content: a re-apply that rewrites five files reports work that did not
// happen.
func TestReapplyWritesNothing(t *testing.T) {
	d := &fakeDistro{files: map[string]string{}}
	apply(t, d, testSettings())

	d.calls = nil
	changes, err := PlanApply(context.Background(), d, "Ubuntu", testSettings(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Changed() {
			t.Errorf("%s reports a change on a second plan", c.File.Path)
		}
	}
	if err := Apply(context.Background(), d, "Ubuntu", changes, time.Second); err != nil {
		t.Fatal(err)
	}
	for _, call := range d.calls {
		if strings.HasPrefix(call, "set -e") || strings.HasPrefix(call, "rm -f") {
			t.Errorf("a second apply wrote something: %s", call)
		}
	}
}

// Revert puts the distribution back: our files gone, the user's file as it was.
func TestRevertRestoresWhatWasThere(t *testing.T) {
	const original = "PATH=/usr/local/bin:/usr/bin\nEDITOR=vim\n"
	d := &fakeDistro{files: map[string]string{"/etc/environment": original}}
	apply(t, d, testSettings())

	changes, err := PlanRevert(context.Background(), d, "Ubuntu", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), d, "Ubuntu", changes, time.Second); err != nil {
		t.Fatal(err)
	}

	if got := d.files["/etc/environment"]; got != original {
		t.Errorf("/etc/environment = %q, want it back as %q", got, original)
	}
	for _, path := range []string{EnvFile, "/etc/profile.d/99-wslkit-proxy.sh", "/etc/apt/apt.conf.d/99wslkit-proxy", "/etc/systemd/system.conf.d/wslkit-proxy.conf"} {
		if _, ok := d.files[path]; ok {
			t.Errorf("%s should have been removed", path)
		}
	}
}

// Reverting a distribution nothing was applied to must not touch it.
func TestRevertLeavesAnUntouchedDistroAlone(t *testing.T) {
	d := &fakeDistro{files: map[string]string{"/etc/environment": "EDITOR=vim\n"}}
	changes, err := PlanRevert(context.Background(), d, "Ubuntu", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Changed() {
			t.Errorf("%s would be changed on a distribution nothing was applied to", c.File.Path)
		}
	}
	if got := d.files["/etc/environment"]; got != "EDITOR=vim\n" {
		t.Errorf("/etc/environment was altered: %q", got)
	}
}

// A file that comes back as something other than base64 means the guest
// answered with an error message. Treating it as content would write that
// message into the user's file.
func TestReadRejectsNonBase64(t *testing.T) {
	d := &brokenDistro{}
	if _, err := PlanApply(context.Background(), d, "Ubuntu", testSettings(), time.Second); err == nil {
		t.Fatal("expected the garbled read to fail")
	}
}

type brokenDistro struct{}

func (brokenDistro) Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error) {
	return "bash: base64: command not found", nil
}

func TestPlanApplyRefusesEmptySettings(t *testing.T) {
	d := &fakeDistro{files: map[string]string{}}
	if _, err := PlanApply(context.Background(), d, "Ubuntu", Settings{}, time.Second); err == nil {
		t.Fatal("there is nothing to apply, and saying so beats writing empty files")
	}
}

func TestMergeAndUnmergeRoundTrip(t *testing.T) {
	const original = "A=1\nB=2\n"
	merged := Merge(original, "http_proxy=x")
	if !strings.Contains(merged, "A=1") || !strings.Contains(merged, "http_proxy=x") {
		t.Fatalf("merged = %q", merged)
	}
	back, found := Unmerge(merged)
	if !found || back != original {
		t.Errorf("Unmerge = %q, %v; want %q", back, found, original)
	}
	// A file with lines after the block keeps them.
	withTail := merged + "C=3\n"
	back, _ = Unmerge(withTail)
	if back != original+"C=3\n" {
		t.Errorf("the lines after the block were lost: %q", back)
	}
	// A file with no block is returned untouched.
	if got, found := Unmerge(original); found || got != original {
		t.Errorf("Unmerge(%q) = %q, %v", original, got, found)
	}
}

// A file with no trailing newline must not have the block glued onto its last
// line.
func TestMergeAddsTheMissingNewline(t *testing.T) {
	got := Merge("A=1", "http_proxy=x")
	if !strings.HasPrefix(got, "A=1\n") {
		t.Errorf("merged = %q", got)
	}
}
