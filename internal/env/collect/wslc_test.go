package collect

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Captured on WSL 2.9.12, with the user name in the session and the path
// replaced.
func TestParseWSLCSessions(t *testing.T) {
	v, names, err := parseWSLCSessions(fixture(t, "wslc-2.9.12-info.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v != "2.9.12.0" || len(names) != 1 || names[0] != "wslc-cli-user" {
		t.Errorf("version %q names %v", v, names)
	}
	// After a reinstall wslc had no session at all; that is an empty list,
	// not an error, and nothing is created to fill it.
	_, names, err = parseWSLCSessions(`{"Client":{"Version":"2.9.12.0"},"Server":{"Sessions":[]}}`)
	if err != nil || len(names) != 0 {
		t.Errorf("names %v err %v", names, err)
	}
	if _, _, err := parseWSLCSessions("Session not found"); err == nil {
		t.Error("text that is not JSON should be an error")
	}
}

// Captured from the session VM on the machine the bug was found on: the host's
// resolver answered SERVFAIL, a public one NOERROR.
func TestParseWSLCDNS(t *testing.T) {
	s := parseWSLCDNS("s", fixture(t, "wslc-2.9.12-dns-servfail.txt"))
	if s.Err != "" {
		t.Fatalf("err %q", s.Err)
	}
	if len(s.Resolvers) != 1 || s.Resolvers[0].Addr != "192.168.1.1" || s.Resolvers[0].Status != "SERVFAIL" {
		t.Errorf("resolvers %+v", s.Resolvers)
	}
	if s.Public.Addr != wslcPublicResolver || s.Public.Status != "NOERROR" {
		t.Errorf("public %+v", s.Public)
	}
}

func TestParseWSLCDNSSaysWhatWentWrong(t *testing.T) {
	if s := parseWSLCDNS("s", "error dig is not in the session VM\n"); s.Err != "dig is not in the session VM" {
		t.Errorf("err %q", s.Err)
	}
	// Nothing printed is not "no resolvers": it is a measurement that did
	// not happen.
	if s := parseWSLCDNS("s", ""); s.Err == "" {
		t.Error("empty output should be an error")
	}
	// A session whose resolv.conf has no IPv4 nameserver prints only the
	// public line, and gives containers nothing.
	s := parseWSLCDNS("s", "public 1.1.1.1 NOERROR\n")
	if s.Err != "" || len(s.Resolvers) != 0 {
		t.Errorf("%+v", s)
	}
}

func TestWSLCDNSScriptHasUnixLineEndings(t *testing.T) {
	if strings.Contains(wslcDNSScript, "\r") {
		t.Fatal("the script contains a carriage return, which the session VM's shell will choke on")
	}
}
