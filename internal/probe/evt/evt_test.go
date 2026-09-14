package evt

import (
	"strings"
	"testing"
	"time"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func evtEnv(events []env.Event, locked bool) *env.Env {
	e := env.New("t")
	e.Events.Window = 7 * 24 * time.Hour
	e.Events.Recent = env.Ok(events, "t")
	for _, ch := range []string{
		"Microsoft-Windows-Hyper-V-Compute-Admin",
		"Microsoft-Windows-Host-Network-Service-Admin",
	} {
		if locked {
			e.Events.Channels[ch] = env.Fail[env.ChannelInfo](env.ErrNeedsElevation, ch, errDenied)
		} else {
			e.Events.Channels[ch] = env.Ok(env.ChannelInfo{Records: 10}, ch)
		}
	}
	return e
}

// The quiet case from an ordinary console. Reporting it as clean without
// saying which logs were shut would be under-reporting with a straight face.
func TestQuietUnelevatedSaysWhatItCouldNotRead(t *testing.T) {
	r := (Crashes{}).Run(evtEnv(nil, true))
	if r.Status != probe.OK {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Detail, "needs an elevated console") {
		t.Errorf("detail = %q", r.Detail)
	}
	if r.Confidence > 0.3 {
		t.Errorf("confidence %v is too high for logs nobody opened", r.Confidence)
	}
}

func TestQuietElevatedIsConfident(t *testing.T) {
	r := (Crashes{}).Run(evtEnv(nil, false))
	if r.Status != probe.OK {
		t.Fatalf("status %s", r.Status)
	}
	if strings.Contains(r.Detail, "needs an elevated console") {
		t.Errorf("nothing was locked: %q", r.Detail)
	}
	if r.Confidence < 0.4 {
		t.Errorf("confidence %v is too low for logs that were read", r.Confidence)
	}
}

// A host network service error is a cause. A crash report is a symptom. When
// both are there, the cause leads.
func TestNetworkErrorLeadsAndNamesTheKnownIssue(t *testing.T) {
	now := time.Now()
	e := evtEnv([]env.Event{
		{Channel: "Microsoft-Windows-Host-Network-Service-Admin", ID: 100, Time: now.Add(-time.Hour), Message: "Failed to create network, error 0x8007054f"},
		{Channel: "Application", Provider: "Application Error", Time: now.Add(-30 * time.Minute), Message: "Faulting application wslservice.exe"},
	}, false)
	r := (Crashes{}).Run(e)
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "host network") {
		t.Errorf("the summary should lead with the cause: %q", r.Summary)
	}
	if !strings.Contains(r.Detail, "crash report") {
		t.Errorf("the symptom should still be reported: %q", r.Detail)
	}
	if len(r.Refs) == 0 || !strings.Contains(strings.Join(r.Refs, " "), "13454") {
		t.Errorf("refs = %v", r.Refs)
	}
	if r.FixHint == "" {
		t.Error("an HNS failure has a known first thing to try")
	}
}

// A compute error without the network involved is still worth reporting, but
// not with the network advice attached to it.
func TestComputeErrorWithoutNetworkAdvice(t *testing.T) {
	e := evtEnv([]env.Event{
		{Channel: "Microsoft-Windows-Hyper-V-Compute-Admin", ID: 4096, Time: time.Now(), Message: "Failed to start the virtual machine"},
	}, false)
	r := (Crashes{}).Run(e)
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if strings.Contains(r.Detail, "0x8007054f") {
		t.Errorf("this is not the network class: %q", r.Detail)
	}
}

// The pre-existing behaviour, which must survive: a crash shortly after a
// resume is the sleep/hibernate class.
func TestCrashAfterResumeStillReported(t *testing.T) {
	now := time.Now()
	e := evtEnv([]env.Event{
		{Channel: "System", Provider: "Microsoft-Windows-Kernel-Power", ID: 107, Time: now.Add(-time.Hour)},
		{Channel: "Application", Provider: "Application Error", Time: now.Add(-55 * time.Minute), Message: "Faulting application vmmemWSL"},
	}, false)
	r := (Crashes{}).Run(e)
	if r.Status != probe.Warn || !strings.Contains(r.Detail, "sleep/hibernate") {
		t.Fatalf("status %s, detail %q", r.Status, r.Detail)
	}
}

var errDenied = errStr("access denied")

type errStr string

func (e errStr) Error() string { return string(e) }

// The shape a healthy machine actually logs, taken from a real elevated
// capture: 23 Host-Network-Service events in a week, all id 1006 carrying
// 0x80070002, on a desktop where WSL starts every time. Leading with those, and
// telling the reader to restart HNS and delete their WSL network, is sending
// them to fix nothing. This is that regression.
func TestRoutineHostNetworkNoiseIsNotAFinding(t *testing.T) {
	now := time.Now()
	var evs []env.Event
	for i := 0; i < 23; i++ {
		evs = append(evs, env.Event{
			Channel: "Microsoft-Windows-Host-Network-Service-Admin", ID: 1006,
			Time:    now.Add(-time.Duration(i) * time.Hour),
			Message: "Parameter0=0x80070002 Parameter1=FB44A04A-2620-43B6-8EBD-4BBD20952880",
		})
	}
	r := (Crashes{}).Run(evtEnv(evs, false))
	if r.Status != probe.OK {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	if r.FixHint != "" {
		t.Errorf("a healthy machine should be given nothing to do: %q", r.FixHint)
	}
	// They are still counted, because somebody investigating a real problem
	// wants to know they are there.
	if !strings.Contains(r.Detail, "23 Hyper-V compute / host network error(s)") {
		t.Errorf("the events should still be reported in the detail: %q", r.Detail)
	}
}

// The same channel with the HRESULT that does stop WSL starting still leads.
func TestFatalHostNetworkErrorStillLeads(t *testing.T) {
	r := (Crashes{}).Run(evtEnv([]env.Event{{
		Channel: "Microsoft-Windows-Host-Network-Service-Admin", ID: 1006, Time: time.Now(),
		Message: "Failed to create network, error 0x8007054f",
	}}, false))
	if r.Status != probe.Warn {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	if r.FixHint == "" {
		t.Error("this one has a known first thing to try")
	}
}
