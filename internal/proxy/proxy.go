// Package proxy works out what proxy a distribution should use, and reports
// what Windows and WSL are each doing about it.
//
// The problem this exists for is not "find the proxy". It is that Windows knows
// the answer and the distribution does not, and the mechanism that is supposed
// to bridge the two only half works:
//
//   - WSL's autoProxy injects the variables into each process it starts, and
//     writes them to no file. A systemd unit, a cron job, anything started by
//     [boot] command, and anything already running never sees them.
//   - A PAC script is passed through as WSL_PAC_URL, unevaluated, because
//     nothing in a headless Linux evaluates PAC. So a machine whose proxy is
//     decided by a script gets a variable naming the script and no proxy.
//   - In NAT mode a proxy on 127.0.0.1 is dropped, because the distribution's
//     loopback is not the host's. A Windows-side local proxy is invisible.
//
// This package is the pure half: what the settings mean, which address the
// distribution has to use, and what to write where. The Windows half reads the
// settings and the registry, and lives beside it.
package proxy

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Settings is the proxy a distribution should be given, already translated into
// addresses that work from inside it.
type Settings struct {
	// HTTP and HTTPS are full URLs, as the environment variables want them.
	HTTP  string
	HTTPS string
	// NoProxy is the bypass list in the form Linux tools expect: a
	// comma-separated list of hosts and domain suffixes.
	NoProxy []string
	// PacURL is set when the configuration is a script rather than an
	// address. Nothing in the distribution can evaluate it, so it is carried
	// only so the report can say that is what happened.
	PacURL string
	// Source explains where this came from, for output that has to be
	// believable.
	Source string
}

// Empty reports whether there is anything to apply.
func (s Settings) Empty() bool { return s.HTTP == "" && s.HTTPS == "" }

// Env renders the settings as environment variables.
//
// Both spellings are written. Which one a program reads is down to its library:
// curl and git take the lower case, some Java and Go code takes the upper, and
// a few tools take whichever they see first. Writing one of them is how a proxy
// ends up working for apt and not for pip.
func (s Settings) Env() []EnvVar {
	var out []EnvVar
	add := func(name, value string) {
		if value == "" {
			return
		}
		out = append(out, EnvVar{Name: strings.ToLower(name), Value: value})
		out = append(out, EnvVar{Name: strings.ToUpper(name), Value: value})
	}
	add("http_proxy", s.HTTP)
	add("https_proxy", s.HTTPS)
	// ftp is long dead, but apt and wget still consult it and an unset
	// ftp_proxy on a machine where everything else is proxied is a puzzle
	// nobody needs.
	add("ftp_proxy", s.HTTP)
	add("all_proxy", s.HTTP)
	if len(s.NoProxy) > 0 {
		add("no_proxy", strings.Join(s.NoProxy, ","))
	}
	return out
}

// EnvVar is one variable to set.
type EnvVar struct {
	Name  string
	Value string
}

// DefaultNoProxy is what every configuration should carry regardless of what
// Windows says.
//
// Loopback and the link-local range are never reachable through a proxy, and a
// distribution that sends its own localhost traffic to a corporate proxy fails
// in ways that take an afternoon to understand. Windows' own bypass list is
// added on top of this.
var DefaultNoProxy = []string{"localhost", "127.0.0.1", "::1", "169.254.0.0/16"}

// HostAddress is how a distribution reaches a service listening on Windows.
//
// In NAT mode the distribution has its own network and its own loopback, so the
// host is the gateway address WSL recorded in the registry. In mirrored mode
// the two share an interface set and 127.0.0.1 in the distribution is the
// host's loopback, which is the whole point of that mode.
func HostAddress(mode, natGateway string) (addr string, why string, err error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "mirrored":
		return "127.0.0.1", "mirrored networking: the distribution shares the host's loopback", nil
	case "none":
		return "", "", fmt.Errorf("proxy: networkingMode=none, so the distribution has no way to reach anything")
	default:
		if natGateway == "" {
			return "", "", fmt.Errorf("proxy: NAT networking, but the gateway address is not in the registry yet. Start a distribution once and try again")
		}
		return natGateway, "NAT networking: the host is the WSL gateway " + natGateway, nil
	}
}

// TranslateForDistro rewrites a Windows-side proxy address into one that works
// from inside a distribution.
//
// A proxy on the host's loopback is the case that matters. From inside a NAT
// distribution, 127.0.0.1 is the distribution itself, so the address has to
// become the gateway; WSL's own autoProxy does not do this and drops the
// setting instead, which is why a local proxy on Windows appears not to exist.
func TranslateForDistro(proxyURL, hostAddr string) (string, bool) {
	if proxyURL == "" || hostAddr == "" {
		return proxyURL, false
	}
	scheme, rest := splitScheme(proxyURL)
	host, port := splitHostPort(rest)
	if !isLoopback(host) {
		return proxyURL, false
	}
	rebuilt := scheme + hostAddr
	if port != "" {
		rebuilt += ":" + port
	}
	return rebuilt, true
}

// isLoopback reports whether a host part refers to the machine it is read on.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// NormalizeProxyURL puts a proxy address into the form the environment
// variables want: a URL with a scheme.
//
// Windows stores them without one ("proxy.corp:8080"), Linux tools want one,
// and a few of them silently do nothing with an address that has no scheme.
func NormalizeProxyURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		return s
	}
	return "http://" + s
}

// ParseWindowsProxy reads the proxy string Windows stores.
//
// It is either one address for everything, or a list of per-scheme addresses
// such as "http=proxy:80;https=secure:443". Both forms turn up in the wild and
// the second one is why reading only the first entry gets HTTPS wrong.
func ParseWindowsProxy(s string) (httpProxy, httpsProxy string) {
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ' ' || r == '\t' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		scheme, addr, ok := strings.Cut(part, "=")
		if !ok {
			// One address for everything.
			if httpProxy == "" {
				httpProxy = NormalizeProxyURL(part)
			}
			if httpsProxy == "" {
				httpsProxy = NormalizeProxyURL(part)
			}
			continue
		}
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "http":
			httpProxy = NormalizeProxyURL(addr)
		case "https":
			httpsProxy = NormalizeProxyURL(addr)
		}
	}
	// A configuration that names only an HTTP proxy is understood by every
	// browser as covering HTTPS through CONNECT as well, and WSL's own code
	// was fixed to do the same (microsoft/WSL PR 40950). Doing anything else
	// leaves HTTPS unproxied on exactly the machines that need it most.
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	return httpProxy, httpsProxy
}

// ParseBypass turns a Windows bypass list into the no_proxy list Linux tools
// expect.
//
// The two are not the same language. Windows separates with semicolons and uses
// the literal "<local>" to mean "any name with no dot in it"; Linux tools take
// commas and have no equivalent, so <local> is expanded into the entries that
// actually cover it. Windows wildcards ("*.corp.example") become the suffix
// form that curl and Go both understand.
func ParseBypass(s string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, entry := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\t' }) {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
		case strings.EqualFold(entry, "<local>"):
			// No Linux tool has this concept. Loopback is the part of it
			// that matters and is covered by the defaults; the rest, a
			// bare intranet hostname, cannot be expressed at all and
			// saying so is better than pretending.
			add("localhost")
		case strings.HasPrefix(entry, "*."):
			add(strings.TrimPrefix(entry, "*"))
		case strings.HasPrefix(entry, "*"):
			add(strings.TrimPrefix(entry, "*"))
		default:
			add(entry)
		}
	}
	return out
}

// MergeNoProxy combines the defaults with what Windows said, in a stable order.
func MergeNoProxy(extra []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range DefaultNoProxy {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	var rest []string
	for _, v := range extra {
		if v != "" && !seen[v] {
			seen[v] = true
			rest = append(rest, v)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func splitScheme(s string) (scheme, rest string) {
	if i := strings.Index(s, "://"); i >= 0 {
		return s[:i+3], s[i+3:]
	}
	return "", s
}

// splitHostPort splits a host:port that may be a bracketed IPv6 literal, and
// may have a path after it.
func splitHostPort(s string) (host, port string) {
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i >= 0 {
			host = s[:i+1]
			if rest := s[i+1:]; strings.HasPrefix(rest, ":") {
				port = rest[1:]
			}
			return host, port
		}
	}
	if h, p, ok := strings.Cut(s, ":"); ok {
		return h, p
	}
	return s, ""
}
