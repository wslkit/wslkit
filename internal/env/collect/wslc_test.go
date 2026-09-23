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
