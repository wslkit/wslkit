package wsl

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslconfig"
	"github.com/wslkit/wslkit/internal/wslver"
)

// Two checks read what a running distribution says about the utility VM
// itself (env.VMFacts): whether nested virtualization reached it, and which
// handler the kernel gives a Windows executable to.

// ---------------------------------------------------------------- KVM001

// Nested reports whether nested virtualization, when WSL is meant to give it,
// actually reached the VM.
//
// WSL turns it off without a word when the host cannot provide it, and in
// October 2026 the cumulative update KB5127648 made it unable to on AMD hosts
// (microsoft/WSL#41917): the processor capability query fails, and up to 3.0.3
// that failure was swallowed. 3.0.4 prints a warning (microsoft/WSL#41942),
// once, at boot. /dev/kvm in the guest is the answer either way.
type Nested struct{}

func (Nested) base() probe.Base {
	return probe.Base{PID: "KVM001", PTitle: "Nested virtualization reaches the VM", PMilestone: "M3", PNeeds: []string{"WSL004"}}
}
func (p Nested) ID() string        { return p.base().ID() }
func (p Nested) Title() string     { return p.base().Title() }
func (p Nested) Milestone() string { return p.base().Milestone() }
func (p Nested) Needs() []string   { return p.base().Needs() }

// nestedQueryBroken is the first build revision of the October 2026 cumulative
// update that broke the capability query on AMD (KB5127648, 26100/26200/26300.9711).
const nestedQueryBroken = 9711

func (p Nested) Run(e *env.Env) probe.Result {
	b := p.base()
	wanted, why := nestedWanted(e)
	if !wanted {
		r := b.Res(probe.OK, 0, "Nested virtualization is not asked for: "+why)
		return r
	}
	if !e.VM.Collected() {
		return b.Res(probe.Skipped, 0, "skipped: the VM was not asked (older snapshot)")
	}
	if !e.VM.OK() {
		r := b.Res(probe.Skipped, 0, "Nested virtualization is on ("+why+"), but no running distribution was asked whether it arrived")
		r.Detail = e.VM.Err
		return r
	}
	v := e.VM.Value
	lines := []string{
		"asked for: " + why,
		fmt.Sprintf("read from %s: /dev/kvm %s, vmx/svm %s, CPU %s", v.ReadFrom, present(v.KVM), present(v.VirtFlags), orUnset(v.CPUVendor)),
	}
	if v.KVM {
		r := b.Res(probe.OK, 0.3, "Nested virtualization is on and /dev/kvm exists in the VM")
		r.Detail = strings.Join(lines, "\n")
		return r
	}
	if v.VirtFlags {
		r := b.Res(probe.Warn, 0.5, "The VM has the processor's virtualization extensions, but no /dev/kvm")
		r.Detail = strings.Join(append(lines,
			"The extensions reached the VM, so WSL's side worked. /dev/kvm is created by the kernel's KVM driver: a custom kernel without KVM, or kvm_intel / kvm_amd not loaded, leaves it missing."), "\n")
		r.FixHint = "inside the distribution: sudo modprobe kvm_amd   (or kvm_intel); with a custom .wslconfig kernel=, build it with CONFIG_KVM"
		return r
	}

	r := b.Res(probe.Warn, 0.6, "Nested virtualization is on, but the VM did not get it: no vmx/svm, no /dev/kvm")
	cause := "WSL drops nested virtualization when the host cannot provide it. When the host says so, wsl.exe prints \"Nested virtualization is not supported on this machine.\" as the VM starts; when the question itself fails, WSL up to 3.0.3 said nothing, and 3.0.4 prints \"Failed to detect nested virtualization support\"."
	if broken, build := octoberUpdateOnAMD(e, v); broken {
		r = b.Res(probe.Warn, 0.8, fmt.Sprintf("Nested virtualization is on, but the VM did not get it: build %s on an AMD processor is the October 2026 update that breaks it", build))
		cause = "KB5127648 (October 2026, builds ending .9711) makes the processor capability query fail on AMD hosts, and WSL then starts the VM without nested virtualization (microsoft/WSL#41917). WSL 3.0.4 says so: \"Failed to detect nested virtualization support, error: ERROR_INVALID_DATA\". The fix has to come from a Windows update; nothing in WSL or the distribution changes it, and downgrading WSL does not either."
		r.Refs = []string{"https://github.com/microsoft/WSL/issues/41917", "https://github.com/microsoft/WSL/pull/41942"}
	}
	r.Detail = strings.Join(append(lines, cause), "\n")
	if r.Refs == nil {
		r.FixHint = "wsl --shutdown, start a distribution and read what wsl.exe prints first; the processor's virtualization support must be on in the firmware"
	} else {
		r.FixHint = "watch for the Windows update that fixes microsoft/WSL#41917; until then nested virtualization is not available on this host"
	}
	return r
}

// nestedWanted decides whether WSL will try to give the VM nested
// virtualization, the way WslCoreConfig does: on by default on Windows 11 and
// amd64, nothing on Windows 10, and a policy or safe mode turning it off.
func nestedWanted(e *env.Env) (bool, string) {
	build := 0
	if e.Host.OS.OK() {
		build = e.Host.OS.Value.Build
	}
	if build > 0 && build < 22000 {
		return false, fmt.Sprintf("WSL never offers it on Windows 10 (build %d)", build)
	}
	cfg := wslconfig.Parse("")
	if e.Config.WslConfig.OK() {
		cfg = wslconfig.Parse(e.Config.WslConfig.Value)
	}
	if v, ok := cfg.Get("wsl2", "safeMode"); ok {
		if on, valid := wslconfig.ParseBool(v); valid && on {
			return false, "safeMode=true in .wslconfig turns it off"
		}
	}
	wanted, why := e.Host.Arch != "arm64", "the default on Windows 11"
	if e.Host.Arch == "arm64" {
		why = "off by default on arm64"
	}
	if v, ok := cfg.Get("wsl2", "nestedVirtualization"); ok {
		if on, valid := wslconfig.ParseBool(v); valid {
			wanted, why = on, fmt.Sprintf("nestedVirtualization=%s in .wslconfig", v)
		}
	}
	if wanted && e.Net.Policy.OK() && e.Net.Policy.Value["AllowNestedVirtualization"] == "0" {
		return false, "group policy AllowNestedVirtualization=0 turns it off"
	}
	return wanted, why
}

// octoberUpdateOnAMD says whether this host has the update that broke the
// capability query on AMD, and names the build.
func octoberUpdateOnAMD(e *env.Env, v env.VMFacts) (bool, string) {
	if !e.Host.OS.OK() || v.CPUVendor != "AuthenticAMD" {
		return false, ""
	}
	os := e.Host.OS.Value
	if os.Build >= 26100 && os.UBR >= nestedQueryBroken {
		return true, fmt.Sprintf("%d.%d", os.Build, os.UBR)
	}
	return false, ""
}

func present(b bool) string {
	if b {
		return "present"
	}
	return "absent"
}

// ---------------------------------------------------------------- BIN001

// Interop reports whether the kernel will hand a Windows executable to WSL.
//
// Running notepad.exe or code from Linux works because WSL registers the
// WSLInterop handler in binfmt_misc for files that start with "MZ". Another
// package can register its own handler for the same bytes (mono's CLR or cli,
// wine), and the kernel tries the most recently registered entry first; on WSL
// 3.0.1 that broke interop outright (microsoft/WSL#41739). The table is the
// kernel's, so a distribution that clears it breaks interop in every other.
type Interop struct{}

func (Interop) base() probe.Base {
	return probe.Base{PID: "BIN001", PTitle: "Windows programs launch from Linux (binfmt_misc)", PMilestone: "M3", PNeeds: []string{"WSL004"}}
}
func (p Interop) ID() string        { return p.base().ID() }
func (p Interop) Title() string     { return p.base().Title() }
func (p Interop) Milestone() string { return p.base().Milestone() }
func (p Interop) Needs() []string   { return p.base().Needs() }

// interopReregisters is the first runtime that registers WSLInterop again after
// systemd-binfmt replaces the table (microsoft/WSL#41780).
var interopReregisters = wslver.MustParse("3.0.2")

const interopName = "WSLInterop"

func (p Interop) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.VM.Collected() {
		return b.Res(probe.Skipped, 0, "skipped: the VM was not asked (older snapshot)")
	}
	if !e.VM.OK() {
		r := b.Res(probe.Skipped, 0, "No running distribution was asked")
		r.Detail = e.VM.Err
		return r
	}
	v := e.VM.Value
	if off := interopOff(e, v.ReadFrom); off {
		return b.Res(probe.OK, 0, fmt.Sprintf("Interop is turned off in %s's wsl.conf, so Windows programs are not expected to launch", v.ReadFrom))
	}
	if !v.BinfmtMounted {
		r := b.Res(probe.Fail, 0.6, "binfmt_misc is not mounted, so Linux cannot hand a Windows program to WSL")
		r.Detail = fmt.Sprintf("Read from %s: /proc/sys/fs/binfmt_misc has no register file. WSL mounts it when the VM starts; something in the distribution unmounted it.", v.ReadFrom)
		r.FixHint = "wsl --shutdown, then start the distribution again"
		return r
	}

	var own *env.BinfmtEntry
	var rivals []env.BinfmtEntry
	for i, x := range v.Binfmt {
		switch {
		case x.Name == interopName:
			own = &v.Binfmt[i]
		case x.Enabled && claimsWindowsExe(x):
			rivals = append(rivals, x)
		}
	}
	lines := []string{fmt.Sprintf("Read from %s; %d binfmt_misc handler(s) registered", v.ReadFrom, len(v.Binfmt))}
	for _, x := range v.Binfmt {
		lines = append(lines, "  "+describeBinfmt(x))
	}
	refs := []string{"https://github.com/microsoft/WSL/issues/41739"}

	if own == nil || !own.Enabled {
		state := "is missing"
		if own != nil {
			state = "is disabled"
		}
		r := b.Res(probe.Fail, 0.7, "WSL's handler for Windows programs "+state+": running notepad.exe or code from Linux fails")
		r.Detail = strings.Join(append(lines,
			"WSL registers WSLInterop once, when the VM starts, and the table is shared by every distribution. A systemd distribution clearing the table as it shuts down, or systemd-binfmt / update-binfmts rewriting it, removes it for all of them."), "\n")
		if own != nil {
			r.FixHint = "for this boot: echo 1 | sudo tee /proc/sys/fs/binfmt_misc/WSLInterop; for good, find what disables it"
		} else {
			r.FixHint = "wsl --shutdown re-registers it when the VM starts again; keep [boot] protectBinfmt=true (the default) in wsl.conf of systemd distributions"
		}
		r.Refs = refs
		return r
	}
	if len(rivals) == 0 {
		r := b.Res(probe.OK, 0.3, "WSLInterop is registered and no other handler claims Windows executables")
		r.Detail = strings.Join(lines, "\n")
		return r
	}

	names := make([]string, 0, len(rivals))
	for _, x := range rivals {
		names = append(names, fmt.Sprintf("%s (%s)", x.Name, orUnset(x.Interpreter)))
	}
	conf := 0.6
	note := "The kernel tries the most recently registered handler first, and /proc does not show the order, so whichever was registered last wins."
	if rt, ok := runtimeVersion(e); ok && rt.AtLeast(interopReregisters) {
		conf = 0.5
		note += fmt.Sprintf(" WSL %s registers WSLInterop again after systemd-binfmt in a systemd distribution with protectBinfmt on (the default), which puts it back in front of what systemd-binfmt loaded; a handler registered after that, by binfmt-support or by hand, still takes over. Measured on 3.0.3: one registered by hand made cmd.exe run under its interpreter.", rt)
	}
	r := b.Res(probe.Warn, conf, fmt.Sprintf("Another handler also claims Windows executables: %s", strings.Join(names, ", ")))
	r.Detail = strings.Join(append(lines,
		"Windows programs start with \"MZ\", and so do .NET assemblies, which is why mono and wine register a handler for the same bytes. When theirs is picked, notepad.exe fails with \"run-detectors: unable to find an interpreter\" or starts under mono or wine.",
		note), "\n")
	r.FixHint = fmt.Sprintf("for this boot: echo 0 | sudo tee /proc/sys/fs/binfmt_misc/%s; for good: sudo update-binfmts --disable %s (binfmt-support), or remove its file from /usr/lib/binfmt.d", rivals[0].Name, rivals[0].Name)
	r.Refs = refs
	return r
}

// claimsWindowsExe says a handler matches what WSLInterop matches: "MZ" at the
// start of the file, or the .exe extension.
func claimsWindowsExe(x env.BinfmtEntry) bool {
	if x.Offset == 0 && strings.HasPrefix(x.Magic, "4d5a") {
		return true
	}
	return strings.EqualFold(strings.TrimPrefix(x.Extension, "."), "exe")
}

func describeBinfmt(x env.BinfmtEntry) string {
	state := "enabled"
	if !x.Enabled {
		state = "disabled"
	}
	match := "magic " + x.Magic
	if x.Extension != "" {
		match = "extension " + x.Extension
	}
	return fmt.Sprintf("%s: %s, %s, interpreter %s", x.Name, state, match, orUnset(x.Interpreter))
}

// interopOff reads [interop] enabled from a distribution's wsl.conf.
func interopOff(e *env.Env, distro string) bool {
	for _, d := range e.DistroList() {
		if d.Name != distro || !d.WslConf.OK() {
			continue
		}
		v, ok := wslconfig.Parse(d.WslConf.Value).Get("interop", "enabled")
		if !ok {
			return false
		}
		on, valid := wslconfig.ParseBool(v)
		return valid && !on
	}
	return false
}
