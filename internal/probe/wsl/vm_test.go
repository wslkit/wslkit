package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

var interopEntry = env.BinfmtEntry{Name: "WSLInterop", Enabled: true, Interpreter: "/init", Flags: "PF", Magic: "4d5a"}

// vmEnv is a Windows 11 amd64 host with one running distribution that
// answered the VM facts script.
func vmEnv(build, ubr int, wslconfig string, v env.VMFacts) *env.Env {
	e := envWithWslConf("")
	e.Runtime.Version = env.Ok("3.0.3.0", "t")
	e.Host.Arch = "amd64"
	e.Host.OS = env.Ok(env.OSBuild{Major: 10, Build: build, UBR: ubr}, "t")
	if wslconfig != "" {
		e.Config.WslConfig = env.Ok(wslconfig, "t")
	} else {
		e.Config.WslConfig = env.Absent[string]("t")
	}
	v.ReadFrom = "Ubuntu"
	e.VM = env.Ok(v, "t")
	return e
}

func TestNestedJudgement(t *testing.T) {
	amdNoKVM := env.VMFacts{CPUVendor: "AuthenticAMD"}
	for _, tc := range []struct {
		name      string
		build     int
		ubr       int
		arch      string
		wslconfig string
		policy    string
		v         env.VMFacts
		want      probe.Status
		summary   string
	}{
		{"Windows 10 never gets it", 19045, 6456, "amd64", "", "", env.VMFacts{}, probe.OK, "Windows 10"},
		{"turned off", 26100, 9711, "amd64", "[wsl2]\nnestedVirtualization=false\n", "", amdNoKVM, probe.OK, "nestedVirtualization=false"},
		{"safe mode", 26100, 9711, "amd64", "[wsl2]\nsafeMode=true\n", "", amdNoKVM, probe.OK, "safeMode"},
		{"policy", 26100, 9711, "amd64", "", "0", amdNoKVM, probe.OK, "AllowNestedVirtualization=0"},
		{"arm64 default", 26100, 9711, "arm64", "", "", amdNoKVM, probe.OK, "arm64"},
		{"working", 26100, 9711, "amd64", "", "", env.VMFacts{KVM: true, VirtFlags: true, CPUVendor: "AuthenticAMD"}, probe.OK, "/dev/kvm exists"},
		{"extensions but no device", 26100, 6000, "amd64", "", "", env.VMFacts{VirtFlags: true, CPUVendor: "GenuineIntel"}, probe.Warn, "no /dev/kvm"},
		{"the October update on AMD (microsoft/WSL#41917)", 26200, 9711, "amd64", "", "", amdNoKVM, probe.Warn, "26200.9711 on an AMD processor"},
		{"Intel on the same build is not that", 26200, 9711, "amd64", "", "", env.VMFacts{CPUVendor: "GenuineIntel"}, probe.Warn, "did not get it: no vmx/svm"},
		{"AMD before the update is not that", 26100, 9600, "amd64", "", "", amdNoKVM, probe.Warn, "did not get it: no vmx/svm"},
	} {
		e := vmEnv(tc.build, tc.ubr, tc.wslconfig, tc.v)
		e.Host.Arch = tc.arch
		if tc.policy != "" {
			e.Net.Policy = env.Ok(map[string]string{"AllowNestedVirtualization": tc.policy}, "t")
		}
		r := (Nested{}).Run(e)
		if r.Status != tc.want || !strings.Contains(r.Summary, tc.summary) {
			t.Errorf("%s: %s %q, want %s containing %q", tc.name, r.Status, r.Summary, tc.want, tc.summary)
		}
	}
}

func TestNestedOctoberUpdateNamesTheIssue(t *testing.T) {
	r := (Nested{}).Run(vmEnv(26100, 9711, "", env.VMFacts{CPUVendor: "AuthenticAMD"}))
	if len(r.Refs) == 0 || r.Refs[0] != "https://github.com/microsoft/WSL/issues/41917" || !strings.Contains(r.Detail, "KB5127648") {
		t.Errorf("refs %v, detail %q", r.Refs, r.Detail)
	}
}

func TestNestedNeedsTheVMToJudge(t *testing.T) {
	e := vmEnv(26100, 9711, "", env.VMFacts{})
	e.VM = env.Fail[env.VMFacts](env.ErrVMWakeRefused, "t", nil)
	if r := (Nested{}).Run(e); r.Status != probe.Skipped {
		t.Errorf("no running distribution is not a finding: %s %q", r.Status, r.Summary)
	}
	e.VM = env.Field[env.VMFacts]{}
	if r := (Nested{}).Run(e); r.Status != probe.Skipped {
		t.Errorf("an older snapshot is not a finding: %s %q", r.Status, r.Summary)
	}
}

func TestInteropJudgement(t *testing.T) {
	disabled := interopEntry
	disabled.Enabled = false
	clr := env.BinfmtEntry{Name: "CLR", Enabled: true, Interpreter: "/usr/bin/mono", Magic: "4d5a"}
	wine := env.BinfmtEntry{Name: "wine", Enabled: true, Interpreter: "/usr/bin/wine", Extension: ".exe"}
	python := env.BinfmtEntry{Name: "python3.14", Enabled: true, Interpreter: "/usr/bin/python3.14", Magic: "2b0e0d0a"}
	offClr := clr
	offClr.Enabled = false
	for _, tc := range []struct {
		name    string
		v       env.VMFacts
		want    probe.Status
		summary string
	}{
		{"as measured on 3.0.3", env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{interopEntry, python}}, probe.OK, "WSLInterop is registered"},
		{"a disabled rival is harmless", env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{interopEntry, offClr}}, probe.OK, "WSLInterop is registered"},
		{"mono (microsoft/WSL#41739)", env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{interopEntry, clr}}, probe.Warn, "CLR (/usr/bin/mono)"},
		{"wine by extension", env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{interopEntry, wine}}, probe.Warn, "wine (/usr/bin/wine)"},
		{"missing", env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{python}}, probe.Fail, "is missing"},
		{"disabled", env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{disabled}}, probe.Fail, "is disabled"},
		{"not mounted", env.VMFacts{}, probe.Fail, "not mounted"},
	} {
		r := (Interop{}).Run(vmEnv(19045, 6456, "", tc.v))
		if r.Status != tc.want || !strings.Contains(r.Summary, tc.summary) {
			t.Errorf("%s: %s %q, want %s containing %q", tc.name, r.Status, r.Summary, tc.want, tc.summary)
		}
	}
}

// Before 3.0.2 nothing put WSLInterop back in front, so a rival was the
// likelier explanation.
func TestInteropRivalWeighsMoreBefore302(t *testing.T) {
	v := env.VMFacts{BinfmtMounted: true, Binfmt: []env.BinfmtEntry{interopEntry, {Name: "cli", Enabled: true, Interpreter: "/usr/lib/binfmt-support/run-detectors", Magic: "4d5a"}}}
	old := vmEnv(19045, 6456, "", v)
	old.Runtime.Version = env.Ok("3.0.1.0", "t")
	cur := vmEnv(19045, 6456, "", v)
	ro, rc := (Interop{}).Run(old), (Interop{}).Run(cur)
	if ro.Status != probe.Warn || rc.Status != probe.Warn || ro.Confidence <= rc.Confidence {
		t.Errorf("3.0.1 %s %.2f, 3.0.3 %s %.2f", ro.Status, ro.Confidence, rc.Status, rc.Confidence)
	}
	if !strings.Contains(rc.FixHint, "update-binfmts --disable cli") {
		t.Errorf("hint %q", rc.FixHint)
	}
}

func TestInteropOffIsNotAFault(t *testing.T) {
	e := vmEnv(19045, 6456, "", env.VMFacts{BinfmtMounted: true})
	list := e.DistroList()
	list[0].WslConf = env.Ok("[interop]\nenabled=false\n", "t")
	if r := (Interop{}).Run(e); r.Status != probe.OK || !strings.Contains(r.Summary, "turned off") {
		t.Errorf("%s %q", r.Status, r.Summary)
	}
}
