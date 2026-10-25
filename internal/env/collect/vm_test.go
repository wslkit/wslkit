package collect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
)

// The fixture is vmScript's output in Ubuntu on WSL 3.0.3, Windows 10: no
// nested virtualization, WSLInterop and Python's own handler registered.
func TestParseVMMeasured(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "vm-3.0.3-ubuntu.txt"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := parseVM(string(b), "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if v.ReadFrom != "Ubuntu" || v.KVM || v.VirtFlags || v.CPUVendor != "GenuineIntel" || !v.BinfmtMounted {
		t.Fatalf("%+v", v)
	}
	if len(v.Binfmt) != 2 {
		t.Fatalf("want 2 entries, got %+v", v.Binfmt)
	}
	want := env.BinfmtEntry{Name: "WSLInterop", Enabled: true, Interpreter: "/init", Flags: "PF", Magic: "4d5a"}
	if v.Binfmt[0] != want {
		t.Errorf("WSLInterop read as %+v", v.Binfmt[0])
	}
	if p := v.Binfmt[1]; p.Name != "python3.14" || p.Flags != "" || p.Magic != "2b0e0d0a" {
		t.Errorf("python read as %+v", p)
	}
}

func TestParseVMReadsEveryShape(t *testing.T) {
	out := "kvm=yes\r\nvirt=yes\r\nvendor=AuthenticAMD\r\nbinfmt=mounted\r\n" +
		"entry=CLR\ne:disabled\ne:interpreter /usr/bin/mono\ne:flags: \ne:offset 0\ne:magic 4D5A\n" +
		"entry=wine-exe\ne:enabled\ne:interpreter /usr/bin/wine\ne:flags: \ne:extension .exe\n"
	v, err := parseVM(out, "Arch")
	if err != nil {
		t.Fatal(err)
	}
	if !v.KVM || !v.VirtFlags || v.CPUVendor != "AuthenticAMD" || len(v.Binfmt) != 2 {
		t.Fatalf("%+v", v)
	}
	if c := v.Binfmt[0]; c.Enabled || c.Magic != "4d5a" || c.Interpreter != "/usr/bin/mono" {
		t.Errorf("a disabled entry, and magic in lower case: %+v", c)
	}
	if w := v.Binfmt[1]; !w.Enabled || w.Extension != ".exe" || w.Magic != "" {
		t.Errorf("an extension entry: %+v", w)
	}
}

func TestParseVMRefusesNothing(t *testing.T) {
	if _, err := parseVM("sh: 1: syntax error\n", "X"); err == nil {
		t.Error("output with no key=value line is not an answer")
	}
}

func TestPickVMDistro(t *testing.T) {
	run := func(name string, def bool, running bool) env.Distro {
		return env.Distro{Name: name, Version: 2, IsDefault: def, Running: env.Ok(running, "t")}
	}
	if n, ok, _, _ := pickVMDistro([]env.Distro{run("a", false, true), run("b", true, true)}); !ok || n != "b" {
		t.Errorf("the default one, when it runs: %q %v", n, ok)
	}
	if n, ok, _, _ := pickVMDistro([]env.Distro{run("a", false, true), run("b", true, false)}); !ok || n != "a" {
		t.Errorf("any running one otherwise: %q %v", n, ok)
	}
	wsl1 := run("old", true, true)
	wsl1.Version = 1
	if _, ok, kind, _ := pickVMDistro([]env.Distro{wsl1, run("b", false, false)}); ok || kind != env.ErrVMWakeRefused {
		t.Errorf("nothing running is never a reason to start one: %v %v", ok, kind)
	}
	unknown := env.Distro{Name: "c", Version: 2, Running: env.Fail[bool](env.ErrTimeout, "t", nil)}
	if _, ok, kind, _ := pickVMDistro([]env.Distro{unknown}); ok || kind != env.ErrTimeout {
		t.Errorf("not knowing is not the same as stopped: %v %v", ok, kind)
	}
}

func TestParseNetshMeasured(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "netsh-dynamicport-tcp.txt"))
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := parseDynamicPorts(string(b))
	if err != nil || dyn != (env.PortSpan{Start: 49152, End: 65535}) {
		t.Fatalf("%+v %v", dyn, err)
	}
	b, err = os.ReadFile(filepath.Join("testdata", "netsh-excludedportrange-tcp.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ex := parseExcludedPorts(string(b))
	want := []env.PortSpan{{50000, 50059}, {56198, 56297}, {60694, 60793}}
	if len(ex) != len(want) {
		t.Fatalf("%+v", ex)
	}
	for i := range want {
		if ex[i] != want[i] {
			t.Errorf("exclusion %d: %+v, want %+v (the administered one's asterisk must not hide it)", i, ex[i], want[i])
		}
	}
}

// netsh labels in the display language; only the numbers are read.
func TestParseNetshIgnoresLabels(t *testing.T) {
	de := "Protokoll tcp Dynamischer Portbereich\n---------------------------------\nStartport       : 1025\nAnzahl der Ports: 64511\n"
	if dyn, err := parseDynamicPorts(de); err != nil || dyn != (env.PortSpan{Start: 1025, End: 65535}) {
		t.Errorf("%+v %v", dyn, err)
	}
	if _, err := parseDynamicPorts("Start Port : 60000\nNumber of Ports : 9000\n"); err == nil {
		t.Error("a range past 65535 is not a range")
	}
}
