//go:build windows

// Package winhttp reads Windows' own proxy configuration, and asks it which
// proxy applies to a given URL.
//
// Windows keeps proxy settings in three places that do not agree: the per-user
// Internet Settings, the machine-wide WinHTTP configuration that `netsh winhttp`
// writes, and whatever a PAC script decides at the moment it is asked. A tool
// that reads one of them and calls it "the proxy" will be wrong for somebody.
//
// The PAC case is the one that matters most here. A PAC script is JavaScript
// evaluated per URL, so there is no single answer to "what is the proxy": there
// is only an answer for github.com, and possibly a different one for an
// internal host. WinHTTP is what evaluates them on Windows, and asking it is the
// only way to get the same answer the rest of the system gets.
package winhttp

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winhttp = windows.NewLazySystemDLL("winhttp.dll")

	procGetIEProxyConfig = winhttp.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procGetDefaultProxy  = winhttp.NewProc("WinHttpGetDefaultProxyConfiguration")
	procOpen             = winhttp.NewProc("WinHttpOpen")
	procCloseHandle      = winhttp.NewProc("WinHttpCloseHandle")
	procGetProxyForURL   = winhttp.NewProc("WinHttpGetProxyForUrl")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
)

// Access types returned in WINHTTP_PROXY_INFO.
const (
	accessTypeDefaultProxy   = 0
	accessTypeNoProxy        = 1
	accessTypeNamedProxy     = 3
	accessTypeAutomaticProxy = 4
)

// Flags for WINHTTP_AUTOPROXY_OPTIONS.
const (
	autoproxyAutoDetect = 0x00000001
	autoproxyConfigURL  = 0x00000002
	// autoDetectDHCP and autoDetectDNSA are the two WPAD discovery methods.
	// Both are asked for, which is what a browser does.
	autoDetectDHCP = 0x00000001
	autoDetectDNSA = 0x00000002
)

// errorAutodetectionFailed is what WinHttpGetProxyForUrl returns when WPAD
// found nothing. It is a normal answer meaning "go direct", not a fault.
const errorAutodetectionFailed = 12180

// ieProxyConfig mirrors WINHTTP_CURRENT_USER_IE_PROXY_CONFIG.
type ieProxyConfig struct {
	AutoDetect    int32 // BOOL
	AutoConfigURL *uint16
	Proxy         *uint16
	ProxyBypass   *uint16
}

// proxyInfo mirrors WINHTTP_PROXY_INFO.
type proxyInfo struct {
	AccessType  uint32
	Proxy       *uint16
	ProxyBypass *uint16
}

// autoproxyOptions mirrors WINHTTP_AUTOPROXY_OPTIONS.
type autoproxyOptions struct {
	Flags                 uint32
	AutoDetectFlags       uint32
	AutoConfigURL         *uint16
	Reserved              uintptr
	Reserved2             uint32
	AutoLogonIfChallenged int32 // BOOL
}

// Config is a proxy configuration as Windows records it, before any PAC script
// has been evaluated.
type Config struct {
	// AutoDetect means WPAD: find a PAC file by DHCP or DNS. It can be set
	// together with an explicit PAC URL, in which case the URL wins.
	AutoDetect bool
	// PacURL is the configured PAC script, if any. Its presence is what
	// makes the rest of this struct an incomplete answer.
	PacURL string
	// Proxy is the static setting, either "host:port" or a list of
	// "scheme=host:port" separated by semicolons or spaces.
	Proxy string
	// Bypass is the list of hosts that go direct, semicolon-separated, with
	// the literal "<local>" meaning any name without a dot.
	Bypass string
}

// Empty reports whether this configuration says anything at all.
func (c Config) Empty() bool {
	return !c.AutoDetect && c.PacURL == "" && c.Proxy == ""
}

// IEProxyConfig reads the per-user Internet Settings, which is what a browser
// and WSL's own autoProxy use.
func IEProxyConfig() (Config, error) {
	var raw ieProxyConfig
	r, _, err := procGetIEProxyConfig.Call(uintptr(unsafe.Pointer(&raw)))
	if r == 0 {
		return Config{}, fmt.Errorf("winhttp: WinHttpGetIEProxyConfigForCurrentUser: %w", err)
	}
	defer func() {
		freeString(raw.AutoConfigURL)
		freeString(raw.Proxy)
		freeString(raw.ProxyBypass)
	}()
	return Config{
		AutoDetect: raw.AutoDetect != 0,
		PacURL:     windows.UTF16PtrToString(raw.AutoConfigURL),
		Proxy:      windows.UTF16PtrToString(raw.Proxy),
		Bypass:     windows.UTF16PtrToString(raw.ProxyBypass),
	}, nil
}

// DefaultProxyConfig reads the machine-wide WinHTTP configuration, which is
// what `netsh winhttp show proxy` prints and what services use.
//
// It is separate from the per-user settings and is often empty on a machine
// where a browser is nevertheless going through a proxy. That difference is
// worth reporting rather than hiding.
func DefaultProxyConfig() (Config, error) {
	var raw proxyInfo
	r, _, err := procGetDefaultProxy.Call(uintptr(unsafe.Pointer(&raw)))
	if r == 0 {
		return Config{}, fmt.Errorf("winhttp: WinHttpGetDefaultProxyConfiguration: %w", err)
	}
	defer func() {
		freeString(raw.Proxy)
		freeString(raw.ProxyBypass)
	}()
	if raw.AccessType != accessTypeNamedProxy {
		return Config{}, nil
	}
	return Config{
		Proxy:  windows.UTF16PtrToString(raw.Proxy),
		Bypass: windows.UTF16PtrToString(raw.ProxyBypass),
	}, nil
}

// Session is an open WinHTTP session. Resolving a URL needs one, and reusing it
// is what makes the PAC script's own cache work: WinHTTP downloads and compiles
// the script once per session rather than once per question.
type Session struct{ h uintptr }

// Open starts a session. The caller must Close it.
func Open(userAgent string) (*Session, error) {
	ua, err := windows.UTF16PtrFromString(userAgent)
	if err != nil {
		return nil, err
	}
	h, _, callErr := procOpen.Call(
		uintptr(unsafe.Pointer(ua)),
		accessTypeNoProxy, // this session talks to the PAC server directly
		0, 0, 0,
	)
	if h == 0 {
		return nil, fmt.Errorf("winhttp: WinHttpOpen: %w", callErr)
	}
	return &Session{h: h}, nil
}

func (s *Session) Close() {
	if s != nil && s.h != 0 {
		_, _, _ = procCloseHandle.Call(s.h)
		s.h = 0
	}
}

// Resolution is the answer for one URL.
type Resolution struct {
	// Direct means no proxy for this URL.
	Direct bool
	// Proxies are the upstreams to try, in order, as "host:port".
	Proxies []string
	// Source says how the answer was reached, for a report that has to be
	// believable.
	Source string
}

// ProxyForURL asks Windows which proxy applies to one URL.
//
// This is the only correct way to answer the question when a PAC script is in
// play, because the script is a program and the answer is whatever it returns
// for that URL. autoLogon should be false for a PAC file on an intranet server
// that would otherwise challenge for credentials on every call; WinHTTP's own
// guidance is to try without first.
func (s *Session) ProxyForURL(url string, cfg Config, autoLogon bool) (Resolution, error) {
	u, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return Resolution{}, err
	}
	opts := autoproxyOptions{}
	source := ""
	if cfg.PacURL != "" {
		pac, err := windows.UTF16PtrFromString(cfg.PacURL)
		if err != nil {
			return Resolution{}, err
		}
		opts.Flags |= autoproxyConfigURL
		opts.AutoConfigURL = pac
		source = "PAC script " + cfg.PacURL
	}
	if cfg.AutoDetect {
		opts.Flags |= autoproxyAutoDetect
		opts.AutoDetectFlags = autoDetectDHCP | autoDetectDNSA
		if source == "" {
			source = "WPAD auto-detection"
		} else {
			source += " (with WPAD as a fallback)"
		}
	}
	if opts.Flags == 0 {
		// Nothing to evaluate. The static configuration is the answer, and
		// working that out is not WinHTTP's job.
		return Resolution{}, ErrNoAutoConfig
	}
	if autoLogon {
		opts.AutoLogonIfChallenged = 1
	}

	var info proxyInfo
	r, _, callErr := procGetProxyForURL.Call(
		s.h,
		uintptr(unsafe.Pointer(u)),
		uintptr(unsafe.Pointer(&opts)),
		uintptr(unsafe.Pointer(&info)),
	)
	if r == 0 {
		if errno, ok := callErr.(windows.Errno); ok && uint32(errno) == errorAutodetectionFailed {
			// WPAD found no script. Going direct is the correct
			// behaviour and every browser does the same.
			return Resolution{Direct: true, Source: "WPAD found no PAC script"}, nil
		}
		return Resolution{}, fmt.Errorf("winhttp: WinHttpGetProxyForUrl(%s): %w", url, callErr)
	}
	defer func() {
		freeString(info.Proxy)
		freeString(info.ProxyBypass)
	}()

	switch info.AccessType {
	case accessTypeNamedProxy:
		return Resolution{Proxies: SplitProxyList(windows.UTF16PtrToString(info.Proxy)), Source: source}, nil
	case accessTypeNoProxy, accessTypeDefaultProxy, accessTypeAutomaticProxy:
		return Resolution{Direct: true, Source: source}, nil
	}
	return Resolution{Direct: true, Source: source}, nil
}

// ErrNoAutoConfig means there is no PAC script and no WPAD, so there is nothing
// to evaluate and the static settings are the whole answer.
var ErrNoAutoConfig = fmt.Errorf("winhttp: no PAC script and no auto-detection configured")

// SplitProxyList splits what WinHTTP returns: proxies separated by semicolons
// or spaces, each optionally carrying a scheme prefix.
func SplitProxyList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ' ' || r == '\t' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// freeString releases a string WinHTTP allocated for us.
func freeString(p *uint16) {
	if p != nil {
		_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	}
}
