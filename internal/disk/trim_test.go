package disk

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseTrimmedBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want uint64
		ok   bool
	}{
		{"util-linux", "/: 1004.8 GiB (1078939029504 bytes) trimmed\n", 1078939029504, true},
		{"no space before the unit", "/: 12 GiB (1024bytes) trimmed", 1024, true},
		{"several lines takes the last", "/boot: 1 GiB (100 bytes) trimmed\n/: 2 GiB (200 bytes) trimmed\n", 200, true},
		{"busybox says nothing", "", 0, false},
		{"no number", "(bytes) trimmed", 0, false},
		// Larger than a uint64: report nothing rather than a wrong figure.
		{"absurd", "(99999999999999999999999 bytes) trimmed", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseTrimmedBytes(c.in)
			if ok != c.ok || got != c.want {
				t.Errorf("got (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestPlanTrimRefusesWSL1(t *testing.T) {
	_, err := PlanTrim(reg("Legacy", func(r *Registration) { r.Version = 1 }))
	if !errors.Is(err, ErrNotWSL2) {
		t.Fatalf("want ErrNotWSL2, got %v", err)
	}
	if !strings.Contains(err.Error(), "--set-version") {
		t.Errorf("the refusal should say how to convert it: %v", err)
	}
}

func TestPlanTrimWarnsThatItLeavesTheDistroRunning(t *testing.T) {
	p, err := PlanTrim(reg("Ubuntu"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "leaves it running") {
		t.Fatalf("warnings: %+v", p.Warnings)
	}
	if err := p.Valid(); err != nil {
		t.Errorf("plan should be valid: %v", err)
	}
}

func TestTrimReportsWhatFstrimSaid(t *testing.T) {
	host := &fakeHost{results: map[string]CommandResult{
		"Ubuntu /sbin/fstrim": {Stdout: "/: 1004.8 GiB (1078939029504 bytes) trimmed\n"},
	}}
	res, err := Trim(context.Background(), env(nil, nil, host), reg("Ubuntu"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes == nil || *res.Bytes != 1078939029504 {
		t.Fatalf("bytes: %v", res.Bytes)
	}
	if res.UsedFallback {
		t.Error("no fallback should have been needed")
	}
	if len(host.calls) != 1 || !strings.Contains(host.calls[0], "-v") {
		t.Errorf("expected one verbose call, got %v", host.calls)
	}
}

// busybox fstrim has no -v. Retrying without it is the difference between
// trimming an Alpine distribution and refusing to.
type scriptedHost struct {
	fakeHost
	responses []CommandResult
	n         int
}

func (h *scriptedHost) RunAsRoot(ctx context.Context, distro string, argv []string, timeout time.Duration) (CommandResult, error) {
	h.calls = append(h.calls, "run "+distro+" "+strings.Join(argv, " "))
	if h.n < len(h.responses) {
		r := h.responses[h.n]
		h.n++
		return r, nil
	}
	return CommandResult{}, nil
}

func TestTrimFallsBackWhenBusyboxRejectsVerbose(t *testing.T) {
	host := &scriptedHost{responses: []CommandResult{
		{ExitCode: 1, Stderr: "fstrim: unrecognized option: v"},
		{ExitCode: 0, Stdout: ""},
	}}
	e := Env{FS: &fakeFS{}, Disks: &fakeDisks{}, Host: host, Clock: &fakeClock{}}
	res, err := Trim(context.Background(), e, reg("Alpine"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UsedFallback {
		t.Error("the fallback should have been recorded")
	}
	if res.Bytes != nil {
		t.Errorf("busybox says nothing, so there is no figure: %v", res.Bytes)
	}
	if len(host.calls) != 2 {
		t.Fatalf("expected two calls, got %v", host.calls)
	}
	if strings.Contains(host.calls[1], "-v") {
		t.Errorf("the retry should have dropped -v: %q", host.calls[1])
	}
}

// Any other failure is real and must not be retried, or a genuine error is
// tried twice and reported as the wrong thing.
func TestTrimDoesNotRetryARealFailure(t *testing.T) {
	host := &scriptedHost{responses: []CommandResult{
		{ExitCode: 32, Stderr: "fstrim: /: FITRIM ioctl failed: Operation not supported"},
	}}
	e := Env{FS: &fakeFS{}, Disks: &fakeDisks{}, Host: host, Clock: &fakeClock{}}
	_, err := Trim(context.Background(), e, reg("Ubuntu"), 0)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("the guest's own message should survive: %v", err)
	}
	if len(host.calls) != 1 {
		t.Errorf("expected exactly one call, got %v", host.calls)
	}
}

// The figure fstrim prints is the free extent, not space reclaimed. Reporting
// it without saying so reads as a promise the tool cannot keep.
func TestRenderTrimAlwaysCarriesTheCaveatWithAFigure(t *testing.T) {
	var b bytes.Buffer
	RenderTrim(&b, TrimResult{Distro: "Ubuntu", Bytes: u64(1078939029504)})
	out := b.String()
	if !strings.Contains(out, TrimmedBytesAreMisleading) {
		t.Errorf("the caveat is missing:\n%s", out)
	}
	if !strings.Contains(out, "wslkit disk compact Ubuntu") {
		t.Errorf("it should point at what actually shrinks the file:\n%s", out)
	}

	// With no figure there is nothing to be misled about, so the caveat is
	// dropped but the pointer stays.
	var b2 bytes.Buffer
	RenderTrim(&b2, TrimResult{Distro: "Alpine"})
	out2 := b2.String()
	if strings.Contains(out2, TrimmedBytesAreMisleading) {
		t.Errorf("no figure, so no caveat:\n%s", out2)
	}
	if !strings.Contains(out2, "did not say how much") {
		t.Errorf("it should say the figure is missing:\n%s", out2)
	}
	if !strings.Contains(out2, "wslkit disk compact Alpine") {
		t.Errorf("the pointer should still be there:\n%s", out2)
	}
}

func TestTrimJSONNamesTheFieldForWhatItIs(t *testing.T) {
	o := TrimJSON(TrimResult{Distro: "Ubuntu", Bytes: u64(100)})
	if _, ok := o["bytes_freed"]; ok {
		t.Error("the figure is not space freed and must not be called that")
	}
	if o["bytes_offered"] != uint64(100) {
		t.Errorf("bytes_offered = %v", o["bytes_offered"])
	}
	if o["note"] != TrimmedBytesAreMisleading {
		t.Error("the note should travel with the figure in JSON too")
	}
	if _, ok := TrimJSON(TrimResult{Distro: "Alpine"})["bytes_offered"]; ok {
		t.Error("absent is absent, not zero")
	}
}
