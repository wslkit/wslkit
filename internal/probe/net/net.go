// Package net holds networking probes (NET***). The rules copy what the WSL
// runtime itself checks in WslCoreVm::ValidateNetworkingMode and
// WslCoreFirewallSupport.cpp, so wslkit can explain a fallback before the
// user sees "falling back to NAT networking".
package net

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslconfig"
)

func All() []probe.Probe { return []probe.Probe{Mirrored{}, DNS{}} }

// Mode returns the configured networking mode (lower-case) and whether it was
// set explicitly. Policy DefaultNetworkingMode wins over .wslconfig.
func Mode(e *env.Env) (mode string, explicit bool, fromPolicy bool) {
	mode = "nat"
	if e.Config.WslConfig.OK() {
		cfg := wslconfig.Parse(e.Config.WslConfig.Value)
		if v, ok := cfg.Get("wsl2", "networkingMode"); ok {
			mode, explicit = strings.ToLower(v), true
		} else if v, ok := cfg.Get("experimental", "networkingMode"); ok {
			mode, explicit = strings.ToLower(v), true
		}
	}
	if e.Net.Policy.OK() {
		if v, ok := e.Net.Policy.Value["DefaultNetworkingMode"]; ok && v != "" {
			return strings.ToLower(v), true, true
		}
	}
	return mode, explicit, false
}

func configBool(e *env.Env, key string) (val bool, set bool) {
	if !e.Config.WslConfig.OK() {
		return false, false
	}
	cfg := wslconfig.Parse(e.Config.WslConfig.Value)
	for _, sec := range []string{"wsl2", "experimental"} {
		if v, ok := cfg.Get(sec, key); ok {
			b, ok := wslconfig.ParseBool(v)
			return b, ok
		}
	}
	return false, false
}

// ---------------------------------------------------------------- NET004

type Mirrored struct{}

func (Mirrored) base() probe.Base {
	return probe.Base{PID: "NET004", PTitle: "Networking mode preconditions (mirrored, Hyper-V firewall)", PMilestone: "M2", PNeeds: []string{"WSL002"}}
}
func (p Mirrored) ID() string        { return p.base().ID() }
func (p Mirrored) Title() string     { return p.base().Title() }
func (p Mirrored) Milestone() string { return p.base().Milestone() }
func (p Mirrored) Needs() []string   { return p.base().Needs() }

func (p Mirrored) Run(e *env.Env) probe.Result {
	b := p.base()
	mode, explicit, fromPolicy := Mode(e)
	var lines, fails, warns []string
	src := "default"
	if explicit {
		src = ".wslconfig"
	}
	if fromPolicy {
		src = "group policy (DefaultNetworkingMode); .wslconfig cannot override it"
	}
	lines = append(lines, fmt.Sprintf("networkingMode=%s (%s)", mode, src))
	if fromPolicy {
		warns = append(warns, "A policy sets DefaultNetworkingMode; whatever .wslconfig says, WSL uses "+mode)
	}

	build := 0
	if e.Host.OS.OK() {
		build = e.Host.OS.Value.Build
	}
	fw := e.Net.HyperVFirewall
	firewallWanted, firewallSet := configBool(e, "firewall")

	switch mode {
	case "mirrored":
		if build > 0 && build < 22621 {
			fails = append(fails, fmt.Sprintf("mirrored networking needs Windows 11 22H2 (build 22621); this host is build %d. WSL prints \"Mirrored networking mode is not supported\" and falls back to NAT", build))
		}
		if e.Host.IPv6Disabled.OK() && e.Host.IPv6Disabled.Value == 0xFF {
			fails = append(fails, "IPv6 is disabled through the registry (Tcpip6\\Parameters\\DisabledComponents = 0xFF); WSL refuses mirrored mode on such hosts and falls back to NAT. Disabling IPv6 per adapter (Set-NetAdapterBinding) is fine")
		}
		if fw.OK() && !fw.Value.V1 && !fw.Value.V2 {
			warns = append(warns, "Hyper-V firewall is not available on this host, so firewall= is ignored and mirrored traffic is not filtered by Windows Firewall rules")
		}
		if fw.OK() && fw.Value.DisabledByRegistry {
			warns = append(warns, "Hyper-V firewall is disabled by MpsSvc\\Parameters\\HyperVFirewallDisable")
		}
		if len(e.Net.VPNAdapters()) > 0 {
			names := adapterNames(e.Net.VPNAdapters())
			if e.Net.Hotfixes.OK() && e.Net.Hotfixes.Value["KB5068861"] {
				warns = append(warns, fmt.Sprintf("VPN adapter up (%s) with KB5068861 installed: this update broke access to VPN resources from mirrored mode (microsoft/WSL#13724). Workarounds: uninstall the KB, or switch to NAT with dnsTunneling", names))
			} else {
				lines = append(lines, "VPN adapter up: "+names+"; if VPN resources are unreachable from WSL, see microsoft/WSL#13724 and #13454")
			}
		}
	case "bridged":
		warns = append(warns, "bridged networking is deprecated since WSL 2.4.5; use mirrored (Windows 11 22H2+) or NAT")
	case "none":
		warns = append(warns, "networkingMode=none: the VM has no network at all (this is a deliberate setting, or a leftover from a failed mirrored configuration)")
	case "nat", "virtioproxy":
		if firewallSet && firewallWanted && fw.OK() && !fw.Value.V2 {
			warns = append(warns, "firewall=true with NAT needs the enterprise Hyper-V firewall (MSFT_NetFirewallHyperVProfile); this host only has "+fwLevel(fw.Value)+", so WSL prints \"Hyper-V firewall is not supported\"")
		}
	default:
		warns = append(warns, fmt.Sprintf("networkingMode=%q is not a known value; WSL falls back to NAT", mode))
	}
	if fw.OK() {
		lines = append(lines, "Hyper-V firewall support: "+fwLevel(fw.Value))
	} else if fw.NeedsElevation() {
		lines = append(lines, "Hyper-V firewall support: needs elevation to query")
	}

	detail := strings.Join(lines, "\n")
	switch {
	case len(fails) > 0:
		r := b.Res(probe.Fail, 0.8, fails[0])
		r.Detail = detail + "\n" + strings.Join(append(fails, warns...), "\n")
		r.FixID, r.FixHint = "wslconfig", "set networkingMode=nat (or remove the key) in .wslconfig, then wsl --shutdown; or fix the listed precondition"
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/10495", "https://learn.microsoft.com/en-us/windows/wsl/networking#mirrored-mode-networking"}
		return r
	case len(warns) > 0:
		r := b.Res(probe.Warn, 0.5, warns[0])
		r.Detail = detail + "\n" + strings.Join(warns, "\n")
		r.FixHint = "review [wsl2] networkingMode / firewall in .wslconfig; wslkit doctor fix wslconfig comments out ignored keys"
		if strings.Contains(warns[0], "KB5068861") {
			r.Refs = []string{"https://github.com/microsoft/WSL/issues/13724"}
		}
		return r
	default:
		r := b.Res(probe.OK, 0.45, fmt.Sprintf("networkingMode=%s and its host preconditions hold", mode))
		r.Detail = detail
		return r
	}
}

func fwLevel(f env.FirewallSupport) string {
	switch {
	case f.DisabledByRegistry:
		return "disabled by registry"
	case f.V2:
		return "v2 (mirrored and NAT)"
	case f.V1:
		return "v1 (mirrored only)"
	default:
		return "none"
	}
}

func adapterNames(as []env.Adapter) string {
	var n []string
	for _, a := range as {
		s := a.Name
		if a.Description != "" && !strings.EqualFold(a.Description, a.Name) {
			s += " / " + a.Description
		}
		n = append(n, s)
	}
	return strings.Join(n, ", ")
}

// ---------------------------------------------------------------- NET002

type DNS struct{}

func (DNS) base() probe.Base {
	return probe.Base{PID: "NET002", PTitle: "DNS and VPN interplay", PMilestone: "M2", PNeeds: []string{"WSL002"}}
}
func (p DNS) ID() string        { return p.base().ID() }
func (p DNS) Title() string     { return p.base().Title() }
func (p DNS) Milestone() string { return p.base().Milestone() }
func (p DNS) Needs() []string   { return p.base().Needs() }

func (p DNS) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Net.Adapters.Collected() {
		return b.Res(probe.Skipped, 0, "skipped: adapters not collected (older snapshot)")
	}
	if !e.Net.Adapters.OK() {
		return b.Res(probe.Unknown, 0.1, "could not enumerate adapters: "+e.Net.Adapters.Err)
	}
	mode, _, _ := Mode(e)
	tunnel, tunnelSet := configBool(e, "dnsTunneling")
	tunnelOn := !tunnelSet || tunnel // default true on Windows 11 22H2+
	build := 0
	if e.Host.OS.OK() {
		build = e.Host.OS.Value.Build
	}
	if build > 0 && build < 22621 {
		tunnelOn = false // key needs 22H2
	}
	vpns := e.Net.VPNAdapters()
	var lines []string
	up := 0
	for _, a := range e.Net.Adapters.Value {
		if !a.Up || a.Loopback {
			continue
		}
		up++
		tag := ""
		if a.VPN {
			tag = "  [VPN]"
		}
		lines = append(lines, fmt.Sprintf("%s (type %d)%s DNS %s", a.Name, a.IfType, tag, strings.Join(a.DNSServers, ",")))
	}
	lines = append(lines, fmt.Sprintf("networkingMode=%s, dnsTunneling %s", mode, onOff(tunnelOn, tunnelSet)))

	if len(vpns) > 0 && mode == "nat" && !tunnelOn {
		r := b.Res(probe.Warn, 0.55, fmt.Sprintf("VPN adapter up (%s) with NAT networking and DNS tunneling off: WSL's DNS usually breaks on this combination", adapterNames(vpns)))
		r.Detail = strings.Join(lines, "\n") + "\nIn NAT mode the Linux resolver points at the WSL gateway; many VPN clients drop that traffic. DNS tunneling proxies queries through Windows instead (Windows 11 22H2+). For older hosts use wsl-vpnkit."
		r.FixID, r.FixHint = "wslconfig", "add to .wslconfig: [wsl2] dnsTunneling=true   (Windows 11 22H2+), else https://github.com/sakai135/wsl-vpnkit"
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/8365", "https://learn.microsoft.com/en-us/windows/wsl/troubleshooting#wsl-has-no-network-connectivity-once-connected-to-a-vpn"}
		return r
	}
	if up == 0 {
		r := b.Res(probe.Warn, 0.4, "No network adapter is up on the host")
		r.Detail = strings.Join(lines, "\n")
		r.FixHint = "connect the host to a network; WSL inherits its connectivity"
		return r
	}
	r := b.Res(probe.OK, 0.4, fmt.Sprintf("%d adapter(s) up, %d VPN", up, len(vpns)))
	r.Detail = strings.Join(lines, "\n")
	return r
}

func onOff(on, set bool) string {
	s := "off"
	if on {
		s = "on"
	}
	if !set {
		s += " (default)"
	}
	return s
}
