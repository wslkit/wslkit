package host

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

func pluginEnv(runtime string, pls ...env.Plugin) *env.Env {
	e := env.New("t")
	e.Runtime.Version = env.Ok(runtime, "t")
	e.Plugins = env.Ok(pls, "HKLM")
	return e
}

func good(name string) env.Plugin {
	return env.Plugin{Name: name, Path: `C:\p\` + name + `.dll`, ValueType: "REG_SZ", Exists: true, Version: "1.0.0.0", Signature: "trusted", EntryPoint: true}
}

func TestPluginsHealthy(t *testing.T) {
	r := (Plugins{}).Run(pluginEnv("2.7.13.0", good("DockerDesktop")))
	if r.Status != probe.OK || !strings.Contains(r.Summary, "signed") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
	e := env.New("t")
	e.Plugins = env.Absent[[]env.Plugin]("HKLM")
	if r := (Plugins{}).Run(e); r.Status != probe.OK {
		t.Fatalf("absent key -> %s", r.Status)
	}
}

func TestPluginsFatalCases(t *testing.T) {
	cases := map[string]env.Plugin{
		"missing":    {Name: "X", Path: `C:\x.dll`, ValueType: "REG_SZ"},
		"unsigned":   {Name: "X", Path: `C:\x.dll`, ValueType: "REG_SZ", Exists: true, Signature: "unsigned", EntryPoint: true},
		"digest":     {Name: "X", Path: `C:\x.dll`, ValueType: "REG_SZ", Exists: true, Signature: "bad_digest", EntryPoint: true},
		"noexport":   {Name: "X", Path: `C:\x.dll`, ValueType: "REG_SZ", Exists: true, Signature: "trusted", EntryPoint: false},
		"exportsErr": {Name: "X", Path: `C:\x.dll`, ValueType: "REG_SZ", Exists: true, Signature: "trusted", ExportsErr: "not a PE file"},
	}
	for name, pl := range cases {
		r := (Plugins{}).Run(pluginEnv("2.7.13.0", pl))
		if r.Status != probe.Fail || !strings.Contains(r.Detail, "fatal error was returned by plugin") {
			t.Errorf("%s: %s %q", name, r.Status, r.Summary)
		}
	}
}

func TestPluginsSkippedCasesAreWarnings(t *testing.T) {
	r := (Plugins{}).Run(pluginEnv("2.7.13.0", env.Plugin{Name: "X", ValueType: "REG_DWORD"}))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "REG_DWORD") {
		t.Fatalf("dword -> %s %q", r.Status, r.Summary)
	}
	dup := good("B")
	dup.Path = `C:\p\A.dll`
	dup.Duplicate = true
	r = (Plugins{}).Run(pluginEnv("2.7.13.0", good("A"), dup))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "duplicate") {
		t.Fatalf("dup -> %s %q", r.Status, r.Summary)
	}
}

func TestPluginsKnownBadPair(t *testing.T) {
	mde := good("MdeWslPlugin")
	r := (Plugins{}).Run(pluginEnv("2.7.12.0", mde))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "2.7.13") {
		t.Fatalf("mde on 2.7.12 -> %s %q", r.Status, r.Summary)
	}
	if r := (Plugins{}).Run(pluginEnv("2.7.13.0", mde)); r.Status != probe.OK {
		t.Fatalf("mde on 2.7.13 -> %s", r.Status)
	}
}
