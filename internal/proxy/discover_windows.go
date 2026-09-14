//go:build windows

package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/wslkit/wslkit/internal/winapi/winhttp"
	"github.com/wslkit/wslkit/internal/wslconfig"
)

// Windows keeps proxy settings in two places that do not agree, and evaluates a
// third at the moment it is asked. Reading one of them and calling it "the
// proxy" is how a tool ends up confidently wrong, so all three are read and
// reported separately.

// Discovery is everything known about proxying on this machine.
type Discovery struct {
	// User is the per-user Internet Settings: what a browser uses, and what
	// WSL's autoProxy reads.
	User winhttp.Config
	// UserErr records a failure to read them rather than reporting none.
	UserErr error
	// Machine is the WinHTTP configuration that `netsh winhttp` writes and
	// services use. It is often empty on a machine that is nevertheless
	// behind a proxy, which is worth seeing rather than hiding.
	Machine    winhttp.Config
	MachineErr error
	// NatGateway is the address a NAT-mode distribution reaches the host on.
	NatGateway string
	// NatNetwork is the subnet it sits in, for the report.
	NatNetwork string
}

const lxssMachineKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss`

// Discover reads what Windows has.
//
// Nothing here fails the whole call: each source that cannot be read is
// recorded as its own error, because a machine with no WSL gateway yet still
// has proxy settings worth reporting, and a machine whose WinHTTP configuration
// is unreadable still has a gateway.
func Discover() Discovery {
	var d Discovery
	d.User, d.UserErr = winhttp.IEProxyConfig()
	d.Machine, d.MachineErr = winhttp.DefaultProxyConfig()
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, lxssMachineKey, registry.READ); err == nil {
		d.NatGateway, _, _ = k.GetStringValue("NatGatewayIpAddress")
		d.NatNetwork, _, _ = k.GetStringValue("NatNetwork")
		k.Close()
	}
	return d
}

// Effective is the configuration to act on: the per-user settings when they say
// anything, and the machine-wide ones otherwise.
//
// That order is WSL's own: autoProxy reads the per-user settings under the
// user's token. A machine-wide setting with nothing per-user is the case of a
// machine configured by `netsh winhttp import`, where using it is better than
// reporting no proxy at all.
func (d Discovery) Effective() (winhttp.Config, string) {
	if !d.User.Empty() {
		return d.User, "Windows per-user Internet Settings"
	}
	if !d.Machine.Empty() {
		return d.Machine, "machine-wide WinHTTP configuration (netsh winhttp)"
	}
	return winhttp.Config{}, "no proxy is configured on this machine"
}

// Override replaces what Windows says, for trying a configuration out before
// committing to it.
type Override struct {
	// PacURL evaluates this script instead of the configured one, which is
	// how a PAC file can be tested before it is deployed to a fleet.
	PacURL string
	// HTTP and HTTPS name the proxy outright, for a machine whose Windows
	// settings are not the ones wanted inside the distribution.
	HTTP  string
	HTTPS string
}

// Empty reports whether the override says anything.
func (o Override) Empty() bool { return o.PacURL == "" && o.HTTP == "" && o.HTTPS == "" }

// Resolve works out the settings to give a distribution.
//
// forURL is the URL a PAC script is evaluated against. A script can return a
// different proxy for every host, so there is no single correct answer; asking
// about one well-known outside address is the closest thing to one, and the
// report says that is what was done.
func (d Discovery) Resolve(mode, forURL string) (Settings, error) {
	return d.ResolveWith(mode, forURL, Override{})
}

// ResolveWith is Resolve with the configuration replaced or partly replaced.
func (d Discovery) ResolveWith(mode, forURL string, ov Override) (Settings, error) {
	hostAddr, why, err := HostAddress(mode, d.NatGateway)
	if err != nil {
		return Settings{}, err
	}
	cfg, source := d.Effective()
	// An override replaces the automatic part outright rather than being
	// merged with it: the point is to see what a given script or address
	// would do, and a half-applied override would answer a question nobody
	// asked.
	if ov.PacURL != "" {
		cfg = winhttp.Config{PacURL: ov.PacURL, Bypass: cfg.Bypass}
		source = "the PAC script given on the command line"
	}
	s := Settings{Source: source, PacURL: cfg.PacURL}

	if ov.HTTP != "" || ov.HTTPS != "" {
		httpProxy := NormalizeProxyURL(ov.HTTP)
		httpsProxy := NormalizeProxyURL(ov.HTTPS)
		if httpsProxy == "" {
			httpsProxy = httpProxy
		}
		if httpProxy == "" {
			httpProxy = httpsProxy
		}
		return d.finish(httpProxy, httpsProxy, ParseBypass(cfg.Bypass),
			hostAddr, why, Settings{Source: "the address given on the command line"}), nil
	}
	if cfg.Empty() {
		return s, nil
	}

	httpProxy, httpsProxy := ParseWindowsProxy(cfg.Proxy)
	bypass := ParseBypass(cfg.Bypass)

	// A script or WPAD decides per URL, so it has to be evaluated rather
	// than read. This is the case WSL gives up on and passes through as
	// WSL_PAC_URL, and the reason a machine with a PAC gets no proxy inside
	// its distributions at all.
	if cfg.PacURL != "" || cfg.AutoDetect {
		if forURL == "" {
			forURL = "https://github.com/"
		}
		sess, serr := winhttp.Open("wslkit/proxy")
		if serr != nil {
			return s, serr
		}
		defer sess.Close()
		res, rerr := sess.ProxyForURL(forURL, cfg, false)
		switch {
		case rerr != nil:
			// A PAC that cannot be fetched or does not parse is a real
			// failure, and one worth naming: everything on the machine
			// is silently going direct.
			return s, fmt.Errorf("evaluating the proxy configuration for %s: %w", forURL, rerr)
		case res.Direct:
			s.Source = fmt.Sprintf("%s: %s says go direct for %s", source, describeAuto(cfg), forURL)
			return s, nil
		case len(res.Proxies) > 0:
			first := NormalizeProxyURL(res.Proxies[0])
			httpProxy, httpsProxy = first, first
			s.Source = fmt.Sprintf("%s: %s returned %s for %s", source, describeAuto(cfg), res.Proxies[0], forURL)
		}
	}

	if httpProxy == "" && httpsProxy == "" {
		return s, nil
	}
	return d.finish(httpProxy, httpsProxy, bypass, hostAddr, why, s), nil
}

// finish translates the addresses for the distribution and fills in the rest.
//
// The translation is the one WSL does not do. A proxy on the host's loopback is
// unreachable from a NAT distribution, and autoProxy drops the setting rather
// than rewriting it, so a local proxy on Windows looks like no proxy at all.
func (d Discovery) finish(httpProxy, httpsProxy string, bypass []string, hostAddr, why string, s Settings) Settings {
	var rewrote bool
	if v, did := TranslateForDistro(httpProxy, hostAddr); did {
		httpProxy, rewrote = v, true
	}
	if v, did := TranslateForDistro(httpsProxy, hostAddr); did {
		httpsProxy, rewrote = v, true
	}
	s.HTTP, s.HTTPS = httpProxy, httpsProxy
	s.NoProxy = MergeNoProxy(bypass)
	if rewrote {
		s.Source += fmt.Sprintf("; the loopback address was rewritten to %s (%s)", hostAddr, why)
	}
	return s
}

// describeAuto names the automatic mechanism in play, for the source line.
func describeAuto(cfg winhttp.Config) string {
	switch {
	case cfg.PacURL != "" && cfg.AutoDetect:
		return "the PAC script " + cfg.PacURL + " (with WPAD as a fallback)"
	case cfg.PacURL != "":
		return "the PAC script " + cfg.PacURL
	default:
		return "WPAD auto-detection"
	}
}

// AutoProxyGaps lists what WSL's own proxy support will not do for this
// configuration, in the user's terms.
//
// Every item is a measured behaviour of the runtime, not a guess, and each one
// is a case where a user reasonably believes their proxy is configured and it
// is not.
func AutoProxyGaps(cfg winhttp.Config, mode string) []string {
	var out []string
	out = append(out, "WSL injects the variables into each process it starts and writes them to no file, so systemd units, cron jobs and anything already running never see them")
	if cfg.PacURL != "" || cfg.AutoDetect {
		what := "a PAC script"
		if cfg.PacURL == "" {
			what = "WPAD auto-detection, which finds a PAC script"
		}
		out = append(out, "the proxy is decided by "+what+", which WSL passes through as WSL_PAC_URL without evaluating: nothing in the distribution reads that variable, so there is no proxy in there at all")
	}
	httpProxy, _ := ParseWindowsProxy(cfg.Proxy)
	if httpProxy != "" && !strings.EqualFold(mode, "mirrored") {
		if _, isLocal := TranslateForDistro(httpProxy, "10.0.0.1"); isLocal {
			out = append(out, "the proxy is on the host's loopback, which WSL drops in NAT mode rather than rewriting, so autoProxy behaves as though no proxy were set")
		}
	}
	return out
}

// NetworkingMode reads which mode WSL is configured for, without collecting
// everything else about the machine.
//
// The rule itself lives in the parser package, shared with the doctor's
// networking check: two commands answering this question differently would be
// worse than either answering it wrongly.
func NetworkingMode() (mode string, source string) {
	cfgText := ""
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".wslconfig")); err == nil {
			cfgText = string(b)
		}
	}
	policy := ""
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\Policies\WSL`, registry.READ); err == nil {
		policy, _, _ = k.GetStringValue("DefaultNetworkingMode")
		k.Close()
	}
	mode, explicit, fromPolicy := wslconfig.NetworkingMode(cfgText, policy)
	switch {
	case fromPolicy:
		return mode, "group policy, which .wslconfig cannot override"
	case explicit:
		return mode, ".wslconfig"
	default:
		return mode, "the default"
	}
}

// EffectiveWith is Effective with an override applied, so a report shows the
// configuration the answer actually came from rather than the one on the
// machine.
func (d Discovery) EffectiveWith(ov Override) (winhttp.Config, string) {
	cfg, source := d.Effective()
	switch {
	case ov.PacURL != "":
		return winhttp.Config{PacURL: ov.PacURL, Bypass: cfg.Bypass}, "the PAC script given on the command line"
	case ov.HTTP != "" || ov.HTTPS != "":
		proxy := ov.HTTP
		if proxy == "" {
			proxy = ov.HTTPS
		}
		return winhttp.Config{Proxy: proxy, Bypass: cfg.Bypass}, "the address given on the command line"
	}
	return cfg, source
}
