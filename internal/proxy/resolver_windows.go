//go:build windows

package proxy

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/wslkit/wslkit/internal/winapi/winhttp"
)

// Asking Windows which proxy applies to a URL means evaluating a PAC script,
// which means running a piece of JavaScript that may itself do a DNS lookup.
// Doing that for every request would make the proxy slower than the network it
// is proxying.
//
// It is cached by host rather than by URL. A PAC script can in principle answer per
// path, and in practice none of them do: every script anybody writes branches
// on the host, the scheme and the time of day. Caching by host is what makes
// the cache useful at all, and the TTL bounds how wrong it can be.

// WindowsResolver answers from Windows' own configuration, with a cache.
type WindowsResolver struct {
	// Config is what to evaluate. It is captured once rather than read per
	// request: a change is picked up by Refresh.
	cfg winhttp.Config
	// TTL is how long an answer is reused.
	TTL time.Duration

	mu      sync.Mutex
	session *winhttp.Session
	entries map[string]cacheEntry
}

type cacheEntry struct {
	res Resolution
	at  time.Time
}

// DefaultResolveTTL is how long a resolution is reused.
//
// Long enough that a build downloading a thousand files evaluates the script
// once, short enough that moving from the office to a coffee shop takes effect
// inside a minute.
const DefaultResolveTTL = time.Minute

// NewWindowsResolver opens a session against the machine's configuration.
//
// The session is kept open on purpose: WinHTTP downloads and compiles the PAC
// script once per session, so throwing it away per request would undo the
// caching this does anyway.
func NewWindowsResolver(cfg winhttp.Config, ttl time.Duration) (*WindowsResolver, error) {
	if ttl <= 0 {
		ttl = DefaultResolveTTL
	}
	s, err := winhttp.Open("wslkit/proxy")
	if err != nil {
		return nil, err
	}
	return &WindowsResolver{cfg: cfg, TTL: ttl, session: s, entries: map[string]cacheEntry{}}, nil
}

// Close releases the session.
func (r *WindowsResolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		r.session.Close()
		r.session = nil
	}
}

// Refresh replaces the configuration and empties the cache, for when the
// machine's settings have changed.
func (r *WindowsResolver) Refresh(cfg winhttp.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.entries = map[string]cacheEntry{}
}

// Upstream answers for one URL.
func (r *WindowsResolver) Upstream(ctx context.Context, rawURL string) (Resolution, error) {
	key, err := cacheKey(rawURL)
	if err != nil {
		return Resolution{}, err
	}

	r.mu.Lock()
	if e, ok := r.entries[key]; ok && time.Since(e.at) < r.TTL {
		r.mu.Unlock()
		return e.res, nil
	}
	cfg, session := r.cfg, r.session
	r.mu.Unlock()

	res, err := resolveOnce(session, cfg, rawURL)
	if err != nil {
		return Resolution{}, err
	}

	r.mu.Lock()
	r.entries[key] = cacheEntry{res: res, at: time.Now()}
	r.mu.Unlock()
	return res, nil
}

// resolveOnce asks Windows, falling back to the static configuration when there
// is no script to evaluate.
func resolveOnce(session *winhttp.Session, cfg winhttp.Config, rawURL string) (Resolution, error) {
	if cfg.PacURL != "" || cfg.AutoDetect {
		if session == nil {
			return Resolution{}, fmt.Errorf("proxy: the resolver has been closed")
		}
		res, err := session.ProxyForURL(rawURL, cfg, false)
		if err != nil {
			// A script that cannot be fetched is a real failure, and the
			// honest thing is to say so rather than quietly go direct:
			// going direct on a network that requires a proxy fails
			// anyway, and with a worse error.
			return Resolution{}, fmt.Errorf("proxy: evaluating the configuration for %s: %w", rawURL, err)
		}
		return Resolution{Direct: res.Direct, Proxies: res.Proxies, Source: res.Source}, nil
	}

	httpProxy, httpsProxy := ParseWindowsProxy(cfg.Proxy)
	u, err := url.Parse(rawURL)
	if err != nil {
		return Resolution{}, err
	}
	chosen := httpProxy
	if u.Scheme == "https" {
		chosen = httpsProxy
	}
	if chosen == "" || bypassed(u.Hostname(), cfg.Bypass) {
		return Resolution{Direct: true, Source: "the static configuration"}, nil
	}
	return Resolution{Proxies: []string{stripScheme(chosen)}, Source: "the static configuration"}, nil
}

// cacheKey is the scheme, host and port. Not the path: no PAC script anybody
// writes branches on one, and keying by full URL would make the cache miss
// every time.
func cacheKey(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("proxy: %q is not a URL: %w", rawURL, err)
	}
	return u.Scheme + "://" + u.Host, nil
}

// bypassed applies the Windows bypass list to a static configuration.
func bypassed(host, bypass string) bool {
	if host == "" {
		return false
	}
	for _, entry := range ParseBypass(bypass) {
		if matchesBypass(host, entry) {
			return true
		}
	}
	// Loopback never goes through a proxy, whatever the list says.
	return isLoopback(host)
}

// matchesBypass compares one host with one bypass entry, which may be a suffix.
func matchesBypass(host, entry string) bool {
	switch {
	case entry == "":
		return false
	case entry[0] == '.':
		return len(host) > len(entry) && hasSuffixFold(host, entry) || equalFold(host, entry[1:])
	default:
		return equalFold(host, entry)
	}
}
