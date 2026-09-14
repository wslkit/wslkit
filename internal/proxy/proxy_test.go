package proxy

import (
	"strings"
	"testing"
)

// Windows writes a proxy setting in two shapes and both turn up in the wild.
// Reading only the first entry of the second shape is how HTTPS ends up
// unproxied on exactly the machines that need it.
func TestParseWindowsProxy(t *testing.T) {
	cases := []struct {
		in                  string
		wantHTTP, wantHTTPS string
	}{
		{"proxy.corp:8080", "http://proxy.corp:8080", "http://proxy.corp:8080"},
		{"http=proxy.corp:8080;https=secure.corp:8443", "http://proxy.corp:8080", "http://secure.corp:8443"},
		{"http=proxy.corp:8080", "http://proxy.corp:8080", "http://proxy.corp:8080"},
		{"http://proxy.corp:8080", "http://proxy.corp:8080", "http://proxy.corp:8080"},
		{"", "", ""},
	}
	for _, c := range cases {
		gotHTTP, gotHTTPS := ParseWindowsProxy(c.in)
		if gotHTTP != c.wantHTTP || gotHTTPS != c.wantHTTPS {
			t.Errorf("ParseWindowsProxy(%q) = %q, %q; want %q, %q", c.in, gotHTTP, gotHTTPS, c.wantHTTP, c.wantHTTPS)
		}
	}
}

// A configuration naming only an HTTP proxy covers HTTPS through CONNECT, which
// is what every browser does and what WSL was fixed to do in PR 40950. Leaving
// https_proxy empty was the reported bug.
func TestHTTPOnlyConfigurationStillCoversHTTPS(t *testing.T) {
	_, https := ParseWindowsProxy("http=proxy.corp:8080")
	if https == "" {
		t.Fatal("HTTPS must fall back to the HTTP proxy")
	}
}

func TestParseBypass(t *testing.T) {
	got := ParseBypass("*.corp.example;10.0.0.1;<local>;  ;*.internal")
	want := []string{".corp.example", "10.0.0.1", "localhost", ".internal"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ParseBypass = %v, want %v", got, want)
	}
}

// Loopback in the bypass list is not optional. A distribution that sends its own
// localhost traffic to a corporate proxy fails in ways that take an afternoon.
func TestMergeNoProxyAlwaysCoversLoopback(t *testing.T) {
	got := MergeNoProxy([]string{".corp.example"})
	for _, want := range []string{"localhost", "127.0.0.1", "::1"} {
		if !contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
	if !contains(got, ".corp.example") {
		t.Errorf("the Windows entry was dropped: %v", got)
	}
	// And no duplicates when Windows says the same thing.
	twice := MergeNoProxy([]string{"localhost", "localhost"})
	n := 0
	for _, v := range twice {
		if v == "localhost" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("localhost appears %d times: %v", n, twice)
	}
}

// The translation WSL does not do, and the reason a local proxy on Windows
// looks like no proxy from inside a NAT distribution.
func TestTranslateForDistro(t *testing.T) {
	cases := []struct {
		in, host, want string
		wantRewrote    bool
	}{
		{"http://127.0.0.1:3128", "172.20.240.1", "http://172.20.240.1:3128", true},
		{"http://localhost:3128", "172.20.240.1", "http://172.20.240.1:3128", true},
		{"http://[::1]:3128", "172.20.240.1", "http://172.20.240.1:3128", true},
		{"127.0.0.1:3128", "172.20.240.1", "172.20.240.1:3128", true},
		{"http://proxy.corp:8080", "172.20.240.1", "http://proxy.corp:8080", false},
		{"http://10.1.2.3:8080", "172.20.240.1", "http://10.1.2.3:8080", false},
		{"", "172.20.240.1", "", false},
	}
	for _, c := range cases {
		got, rewrote := TranslateForDistro(c.in, c.host)
		if got != c.want || rewrote != c.wantRewrote {
			t.Errorf("TranslateForDistro(%q) = %q, %v; want %q, %v", c.in, got, rewrote, c.want, c.wantRewrote)
		}
	}
}

func TestHostAddress(t *testing.T) {
	if addr, _, err := HostAddress("mirrored", ""); err != nil || addr != "127.0.0.1" {
		t.Errorf("mirrored = %q, %v", addr, err)
	}
	if addr, _, err := HostAddress("nat", "172.20.240.1"); err != nil || addr != "172.20.240.1" {
		t.Errorf("nat = %q, %v", addr, err)
	}
	// Empty means NAT, which is WSL's default.
	if addr, _, err := HostAddress("", "172.20.240.1"); err != nil || addr != "172.20.240.1" {
		t.Errorf("default = %q, %v", addr, err)
	}
	// A gateway that is not in the registry yet cannot be guessed at.
	if _, _, err := HostAddress("nat", ""); err == nil {
		t.Error("NAT with no gateway should fail rather than invent an address")
	}
	if _, _, err := HostAddress("none", ""); err == nil {
		t.Error("networkingMode=none has no answer")
	}
}

// Which spelling a program reads is down to its library, so both are written.
// Writing one of them is how a proxy works for apt and not for pip.
func TestEnvWritesBothSpellings(t *testing.T) {
	s := Settings{HTTP: "http://p:8080", HTTPS: "http://p:8443", NoProxy: []string{"localhost"}}
	seen := map[string]string{}
	for _, v := range s.Env() {
		seen[v.Name] = v.Value
	}
	for _, name := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "no_proxy", "NO_PROXY"} {
		if seen[name] == "" {
			t.Errorf("%s was not set: %v", name, seen)
		}
	}
	if seen["https_proxy"] != "http://p:8443" {
		t.Errorf("https_proxy = %q", seen["https_proxy"])
	}
}

func TestNormalizeProxyURL(t *testing.T) {
	for in, want := range map[string]string{
		"proxy:8080":         "http://proxy:8080",
		"http://proxy:8080":  "http://proxy:8080",
		"https://proxy:8443": "https://proxy:8443",
		"":                   "",
	} {
		if got := NormalizeProxyURL(in); got != want {
			t.Errorf("NormalizeProxyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
