package top

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

var boot = time.Date(2026, 9, 22, 20, 51, 22, 240_000_000, time.UTC)

// Measured on one machine: the utility VM's vmmem was created 0.5 s before its
// guest said it booted, and a wslc session VM booted an hour and a half later.
func TestMatchHostFindsTheUtilityVMByBootTime(t *testing.T) {
	utility := HostVM{PID: 23688, Created: boot.Add(-540 * time.Millisecond), WorkingSetBytes: 700 << 20}
	session := HostVM{PID: 34040, Created: boot.Add(95 * time.Minute), WorkingSetBytes: 750 << 20}
	h := MatchHost([]HostVM{session, utility}, boot)
	if h.Utility == nil || h.Utility.PID != 23688 {
		t.Fatalf("utility %+v", h.Utility)
	}
	if len(h.Others) != 1 || h.Others[0].PID != 34040 {
		t.Errorf("others %+v", h.Others)
	}
}

// Two VMs that booted together cannot be told apart, and claiming either would
// charge one VM's memory to the other.
func TestMatchHostClaimsNeitherOfTwoVMsBootedTogether(t *testing.T) {
	a := HostVM{PID: 1, Created: boot}
	b := HostVM{PID: 2, Created: boot.Add(time.Second)}
	h := MatchHost([]HostVM{a, b}, boot)
	if h.Utility != nil {
		t.Errorf("claimed %+v out of an ambiguous pair", h.Utility)
	}
	if len(h.Others) != 2 {
		t.Errorf("others %+v", h.Others)
	}
}

func TestMatchHostWithNoMatch(t *testing.T) {
	h := MatchHost([]HostVM{{PID: 9, Created: boot.Add(time.Hour)}}, boot)
	if h.Utility != nil || len(h.Others) != 1 {
		t.Errorf("got %+v", h)
	}
}

func TestParseCIMDateTime(t *testing.T) {
	// 15:51:21.702 at UTC-5 is 20:51:21.702 UTC.
	got, err := ParseCIMDateTime("20260922155121.702000-300")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 22, 20, 51, 21, 702_000_000, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
	east, _ := ParseCIMDateTime("20260922155121.000000+060")
	if !east.Equal(time.Date(2026, 9, 22, 14, 51, 21, 0, time.UTC)) {
		t.Errorf("east of UTC: got %v", east.UTC())
	}
	for _, bad := range []string{"", "20260922155121", "20260922155121.702000x300"} {
		if _, err := ParseCIMDateTime(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// hostRunner is a fakeRunner that can also see Windows.
type hostRunner struct {
	fakeRunner
	vms []HostVM
}

func (h *hostRunner) HostVMs(ctx context.Context) ([]HostVM, error) { return h.vms, nil }

// The whole path: a sweep that measured the guest at 5032.24 s of uptime finds
// the vmmem created that long before, through the same Collect the command uses.
func TestCollectMatchesTheUtilityVM(t *testing.T) {
	r := &hostRunner{fakeRunner: fakeRunner{
		running: []string{"skrog-engine"},
		out:     map[string]string{"skrog-engine": realOutput(t)},
	}}
	// Created 5032.24 s before now, which is what the fixture's vm_at_csec says.
	r.vms = []HostVM{
		{PID: 7, Created: time.Now().Add(-503224 * 10 * time.Millisecond), WorkingSetBytes: 800 << 20},
		{PID: 8, Created: time.Now().Add(-time.Minute), WorkingSetBytes: 700 << 20},
	}
	report, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Host == nil || report.Host.Utility == nil || report.Host.Utility.PID != 7 {
		t.Fatalf("host %+v", report.Host)
	}
	var b bytes.Buffer
	Render(&b, report)
	for _, want := range []string{"Windows charges it 800.0 MiB", "1 other VM(s) hold 700.0 MiB more"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q:\n%s", want, b.String())
		}
	}
}

// A wslc session holds memory with no distribution running at all, and the
// report must not say nothing is there.
func TestCollectWithNoDistributionStillReportsOtherVMs(t *testing.T) {
	r := &hostRunner{vms: []HostVM{{PID: 8, Created: boot, WorkingSetBytes: 750 << 20}}}
	report, err := Collect(context.Background(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Host == nil || len(report.Host.Others) != 1 {
		t.Fatalf("host %+v", report.Host)
	}
	var b bytes.Buffer
	Render(&b, report)
	if !strings.Contains(b.String(), "no distributions are running") || !strings.Contains(b.String(), "750.0 MiB more") {
		t.Errorf("got:\n%s", b.String())
	}
}

func TestHostJSON(t *testing.T) {
	if HostJSON(nil) != nil {
		t.Error("an unread host should add nothing")
	}
	o := HostJSON(&Host{Utility: &HostVM{PID: 1, Created: boot, WorkingSetBytes: 5}})
	if o["utility_vm"].(map[string]any)["working_set_bytes"] != uint64(5) {
		t.Errorf("got %+v", o)
	}
	if others := o["other_vms"].([]map[string]any); len(others) != 0 {
		t.Errorf("others %+v", others)
	}
}
