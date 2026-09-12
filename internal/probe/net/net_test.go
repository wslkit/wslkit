package net

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func netEnv(build int, wslconfig string) *env.Env {
	e := env.New("t")
	e.Host.OS = env.Ok(env.OSBuild{Major: 10, Build: build}, "t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	if wslconfig != "" {
		e.Config.WslConfig = env.Ok(wslconfig, "t")
	} else {
		e.Config.WslConfig = env.Absent[string]("t")
	}
	e.Host.IPv6Disabled = env.Ok(uint32(0), "t")
	e.Net.HyperVFirewall = env.Ok(env.FirewallSupport{}, "t")
	e.Net.Adapters = env.Ok([]env.Adapter{
		{Name: "Ethernet", IfType: 6, Up: true, HasIPv4: true, DNSServers: []string{"192.168.1.1"}},
	}, "t")
	e.Net.Hotfixes = env.Ok(map[string]bool{}, "t")
	e.Net.Policy = env.Absent[map[string]string]("t")
	return e
}

func withVPN(e *env.Env) *env.Env {
	e.Net.Adapters.Value = append(e.Net.Adapters.Value, env.Adapter{Name: "Cisco AnyConnect", IfType: 53, Up: true, VPN: true, HasIPv4: true, DNSServers: []string{"10.0.0.2"}})
	return e
}

func TestMirroredOnWindows10Fails(t *testing.T) {
	r := (Mirrored{}).Run(netEnv(19045, "[wsl2]\nnetworkingMode=mirrored\n"))
	if r.Status != probe.Fail || !strings.Contains(r.Summary, "22H2") || r.FixID != "wslconfig" {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}

func TestMirroredIPv6Disabled(t *testing.T) {
	e := netEnv(26100, "[wsl2]\nnetworkingMode=mirrored\n")
	e.Host.IPv6Disabled = env.Ok(uint32(0xFF), "t")
	e.Net.HyperVFirewall = env.Ok(env.FirewallSupport{V1: true, V2: true}, "t")
	r := (Mirrored{}).Run(e)
	if r.Status != probe.Fail || !strings.Contains(r.Summary, "IPv6") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}

func TestMirroredKBRegression(t *testing.T) {
	e := withVPN(netEnv(26100, "[wsl2]\nnetworkingMode=mirrored\n"))
	e.Net.HyperVFirewall = env.Ok(env.FirewallSupport{V1: true, V2: true}, "t")
	e.Net.Hotfixes = env.Ok(map[string]bool{"KB5068861": true}, "t")
	r := (Mirrored{}).Run(e)
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "KB5068861") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
	e.Net.Hotfixes.Value["KB5068861"] = false
	if r := (Mirrored{}).Run(e); r.Status != probe.OK || !strings.Contains(r.Detail, "VPN adapter up") {
		t.Fatalf("without KB -> %s %q", r.Status, r.Detail)
	}
}

func TestPolicyOverridesConfig(t *testing.T) {
	e := netEnv(26100, "[wsl2]\nnetworkingMode=mirrored\n")
	e.Net.Policy = env.Ok(map[string]string{"DefaultNetworkingMode": "nat"}, "t")
	mode, _, fromPolicy := Mode(e)
	if mode != "nat" || !fromPolicy {
		t.Fatalf("mode=%s policy=%v", mode, fromPolicy)
	}
	r := (Mirrored{}).Run(e)
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "policy") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}

func TestNatFirewallWithoutV2(t *testing.T) {
	e := netEnv(26100, "[wsl2]\nfirewall=true\n")
	e.Net.HyperVFirewall = env.Ok(env.FirewallSupport{V1: true}, "t")
	r := (Mirrored{}).Run(e)
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "Hyper-V firewall is not supported") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
	if r := (Mirrored{}).Run(netEnv(26100, "")); r.Status != probe.OK {
		t.Fatalf("defaults -> %s %q", r.Status, r.Summary)
	}
}

func TestDNSVpnNat(t *testing.T) {
	// Windows 10: dnsTunneling unavailable, NAT + VPN -> WARN pointing at wsl-vpnkit.
	r := (DNS{}).Run(withVPN(netEnv(19045, "")))
	if r.Status != probe.Warn || !strings.Contains(r.FixHint, "wsl-vpnkit") {
		t.Fatalf("%s %q", r.Status, r.FixHint)
	}
	// Windows 11 default (tunneling on) -> OK.
	if r := (DNS{}).Run(withVPN(netEnv(26100, ""))); r.Status != probe.OK {
		t.Fatalf("win11 default -> %s %q", r.Status, r.Summary)
	}
	// Explicitly off on Windows 11 -> WARN.
	if r := (DNS{}).Run(withVPN(netEnv(26100, "[wsl2]\ndnsTunneling=false\n"))); r.Status != probe.Warn {
		t.Fatalf("tunneling off -> %s", r.Status)
	}
	// Uncollected adapters -> skipped.
	e := env.New("t")
	if r := (DNS{}).Run(e); r.Status != probe.Skipped {
		t.Fatalf("uncollected -> %s", r.Status)
	}
}
