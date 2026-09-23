package wslc

import (
	"errors"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func withSessions(sessions ...env.WSLCSession) *env.Env {
	e := env.New("test")
	e.WSLC = env.Ok(env.WSLCInfo{Version: "2.9.12.0", Sessions: sessions}, "test")
	return e
}

func session(public string, resolvers ...string) env.WSLCSession {
	s := env.WSLCSession{Name: "s", Running: true, Public: env.WSLCResolver{Addr: "1.1.1.1", Status: public}}
	for i := 0; i+1 < len(resolvers); i += 2 {
		s.Resolvers = append(s.Resolvers, env.WSLCResolver{Addr: resolvers[i], Status: resolvers[i+1]})
	}
	return s
}

// The machine this was found on: the only resolver a container gets answers
// SERVFAIL, and a public one works.
func TestWarnsWhenContainersCannotResolve(t *testing.T) {
	r := (DNS{}).Run(withSessions(session("NOERROR", "192.168.1.1", "SERVFAIL")))
	if r.Status != probe.Warn {
		t.Fatalf("status %s: %s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "192.168.1.1 answers SERVFAIL") {
		t.Errorf("summary %q", r.Summary)
	}
	if !strings.Contains(r.FixHint, "--dns 1.1.1.1") {
		t.Errorf("fix %q", r.FixHint)
	}
}

// A session whose VM is stopped was not asked anything, because asking would
// have started it. That is not a pass, and the probe says so.
func TestSkipsWhenNoSessionVMIsRunning(t *testing.T) {
	stopped := env.WSLCSession{Name: "s", Resolvers: []env.WSLCResolver{}}
	r := (DNS{}).Run(withSessions(stopped))
	if r.Status != probe.Skipped || !strings.Contains(r.Summary, "not start one") {
		t.Errorf("%s: %s", r.Status, r.Summary)
	}
	// A running one beside it is still judged.
	r = (DNS{}).Run(withSessions(stopped, session("NOERROR", "192.168.1.1", "SERVFAIL")))
	if r.Status != probe.Warn {
		t.Errorf("the running session was not judged: %s: %s", r.Status, r.Summary)
	}
}

func TestSkipsWhatItCannotJudge(t *testing.T) {
	older := env.New("test")
	absent := env.New("test")
	absent.WSLC = env.Absent[env.WSLCInfo]("wslc.exe")
	none := withSessions()

	for name, e := range map[string]*env.Env{"older snapshot": older, "not installed": absent, "no session": none} {
		if r := (DNS{}).Run(e); r.Status != probe.Skipped {
			t.Errorf("%s: %s: %s", name, r.Status, r.Summary)
		}
	}
	failed := env.New("test")
	failed.WSLC = env.Fail[env.WSLCInfo](env.ErrOther, "wslc info", errors.New("boom"))
	if r := (DNS{}).Run(failed); r.Status != probe.Unknown {
		t.Errorf("a wslc that would not answer: %s", r.Status)
	}
}

func TestOKWhenAnyResolverAnswers(t *testing.T) {
	r := (DNS{}).Run(withSessions(session("NOERROR", "192.168.1.1", "SERVFAIL", "10.0.0.1", "NOERROR")))
	if r.Status != probe.OK {
		t.Errorf("one working resolver is enough: %s: %s", r.Status, r.Summary)
	}
	// NXDOMAIN is an answer: the resolver is resolving.
	if r := (DNS{}).Run(withSessions(session("NOERROR", "10.0.0.1", "NXDOMAIN"))); r.Status != probe.OK {
		t.Errorf("NXDOMAIN: %s", r.Status)
	}
}

// When even the public resolver fails, the VM has no network, and the probe
// must not blame DNS or recommend a resolver that just failed.
func TestNoNetworkIsNotBlamedOnDNS(t *testing.T) {
	r := (DNS{}).Run(withSessions(session("timeout", "192.168.1.1", "timeout")))
	if r.Status != probe.Warn || !strings.Contains(r.Detail, "no network") {
		t.Fatalf("%s: %s\n%s", r.Status, r.Summary, r.Detail)
	}
	if strings.Contains(r.FixHint, "--dns") {
		t.Errorf("recommended a resolver that did not answer: %q", r.FixHint)
	}
}

func TestNoIPv4ResolverAtAll(t *testing.T) {
	r := (DNS{}).Run(withSessions(session("NOERROR")))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "no IPv4 resolver") {
		t.Errorf("%s: %s", r.Status, r.Summary)
	}
}

func TestASessionThatCouldNotBeMeasuredIsUnknown(t *testing.T) {
	s := env.WSLCSession{Name: "s", Err: "timed out waiting for the session VM"}
	if r := (DNS{}).Run(withSessions(s)); r.Status != probe.Unknown {
		t.Errorf("%s: %s", r.Status, r.Summary)
	}
}
