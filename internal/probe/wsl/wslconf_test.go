package wsl

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/data"
	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// envWithWslConf describes a machine with one running distribution whose
// wsl.conf is the given text.
func envWithWslConf(text string) *env.Env {
	e := env.New("t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	d := env.Distro{GUID: "{a}", Name: "Ubuntu", Version: 2, IsDefault: true}
	d.Running = env.Ok(true, "t")
	d.WslConf = env.Ok(text, "t")
	e.Distros = env.Ok([]env.Distro{d}, "t")
	return e
}

func TestWslConfCleanFile(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[boot]\nsystemd=true\n\n[user]\ndefault=zoe\n"))
	if r.Status != probe.OK {
		t.Fatalf("a valid file should pass: %s %q", r.Status, r.Summary)
	}
}

// No file at all is the normal case: every setting in it has a default.
func TestWslConfAbsentIsFine(t *testing.T) {
	e := envWithWslConf("")
	list := e.DistroList()
	list[0].WslConf = env.Absent[string]("p")
	e.Distros = env.Ok(list, "t")
	if r := (WslConf{}).Run(e); r.Status != probe.OK {
		t.Fatalf("no wsl.conf should pass: %s %q", r.Status, r.Summary)
	}
}

// WSL warns about an unknown key once, on the console, the one time the
// distribution starts. By the time anyone is puzzled, nothing is saying so.
// Finding it, and saying what it should have been, is the whole point.
func TestWslConfCatchesAMisspelledKey(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[boot]\nsystmd=true\n"))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "boot.systmd") {
		t.Errorf("the summary should name the key: %q", r.Summary)
	}
	// And say what it probably should have been.
	if !strings.Contains(r.Detail, "Did you mean boot.systemd?") {
		t.Errorf("the detail should suggest the real key: %q", r.Detail)
	}
}

// A whole section that WSL does not read means every line under it is ignored,
// which is worth distinguishing from one bad key.
func TestWslConfCatchesAnUnknownSection(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[booot]\nsystemd=true\n"))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "no [booot] section") {
		t.Errorf("summary %q", r.Summary)
	}
	if !strings.Contains(r.Detail, "nothing under it applies") {
		t.Errorf("the detail should say the whole block is ignored: %q", r.Detail)
	}
}

// A value WSL cannot parse is treated as absent, so the default applies while
// the file appears to say otherwise.
func TestWslConfCatchesANonBoolean(t *testing.T) {
	// WSL accepts true, false, 1 and 0. These are the spellings people reach
	// for that it does not accept.
	for _, value := range []string{"yes", "no", "on", "off", "enabled"} {
		r := (WslConf{}).Run(envWithWslConf("[boot]\nsystemd=" + value + "\n"))
		if r.Status != probe.Warn {
			t.Errorf("%q should be flagged, got %s", value, r.Status)
			continue
		}
		if !strings.Contains(r.Summary, "not a boolean") {
			t.Errorf("%q: summary %q", value, r.Summary)
		}
		if !strings.Contains(r.Detail, "the default (false) applies") {
			t.Errorf("%q: the detail should say what happens instead: %q", value, r.Detail)
		}
	}
}

// Values may be quoted, and WSL strips the quotes, so the lint has to compare
// what WSL would actually see rather than the raw text.
func TestWslConfAcceptsAQuotedValue(t *testing.T) {
	if r := (WslConf{}).Run(envWithWslConf("[boot]\nsystemd=\"true\"\n")); r.Status != probe.OK {
		t.Fatalf("a quoted boolean should be accepted: %s %q", r.Status, r.Summary)
	}
}

// Matching is case-insensitive, because that is how WSL reads the file.
func TestWslConfIsCaseInsensitive(t *testing.T) {
	if r := (WslConf{}).Run(envWithWslConf("[BOOT]\nSystemD=TRUE\n")); r.Status != probe.OK {
		t.Fatalf("case should not matter: %s %q", r.Status, r.Summary)
	}
}

// WSL keeps the first of a repeated key, so the second line does nothing. People
// usually expect the opposite.
func TestWslConfCatchesADuplicateKey(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[boot]\nsystemd=true\nsystemd=false\n"))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "set twice") {
		t.Errorf("summary %q", r.Summary)
	}
}

// A path setting is read inside the distribution, so a Windows path there is a
// mistake worth naming.
func TestWslConfCatchesARelativePath(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[automount]\nroot=mnt\n"))
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "not an absolute path") {
		t.Fatalf("%s %q", r.Status, r.Summary)
	}
}

// A distribution that is not running cannot be read, and saying so is better
// than reporting the defaults as though they were the file.
func TestWslConfSkipsAStoppedDistribution(t *testing.T) {
	e := env.New("t")
	d := env.Distro{GUID: "{a}", Name: "Ubuntu", Version: 2}
	d.Running = env.Ok(false, "t")
	d.WslConf = env.Fail[string](env.ErrVMWakeRefused, "p", errString("the distribution is stopped"))
	e.Distros = env.Ok([]env.Distro{d}, "t")

	r := (WslConf{}).Run(e)
	if r.Status != probe.Skipped {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "not running") {
		t.Errorf("the summary should say why: %q", r.Summary)
	}
}

// One stopped distribution must not hide a finding in a running one.
func TestWslConfChecksTheRunningOneEvenWhenAnotherIsStopped(t *testing.T) {
	e := env.New("t")
	e.Runtime.Version = env.Ok("2.7.13.0", "t")
	stopped := env.Distro{GUID: "{a}", Name: "Stopped", Version: 2}
	stopped.Running = env.Ok(false, "t")
	stopped.WslConf = env.Fail[string](env.ErrVMWakeRefused, "p", errString("stopped"))
	running := env.Distro{GUID: "{b}", Name: "Live", Version: 2}
	running.Running = env.Ok(true, "t")
	running.WslConf = env.Ok("[boot]\nsystmd=true\n", "t")
	e.Distros = env.Ok([]env.Distro{stopped, running}, "t")

	r := (WslConf{}).Run(e)
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "Live") {
		t.Errorf("the finding should name the distribution it came from: %q", r.Summary)
	}
}

func TestWslConfWithNoDistributions(t *testing.T) {
	e := env.New("t")
	e.Distros = env.Ok([]env.Distro{}, "t")
	if r := (WslConf{}).Run(e); r.Status != probe.OK {
		t.Fatalf("status %s", r.Status)
	}
}

// The finding has to say what to do, and for this file that includes the part
// people forget: it does not apply until the distribution restarts.
func TestWslConfFixHintMentionsTheRestart(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[boot]\nsystmd=true\n"))
	if !strings.Contains(r.FixHint, "wsl --terminate") {
		t.Errorf("fix hint %q", r.FixHint)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// The worst thing that can be wrong with this file. WSL stops reading at the
// first line it cannot parse, so a typo halfway down silently reverts every
// setting below it to its default. .wslconfig does not behave this way, which
// is exactly why people do not expect it here.
func TestWslConfMalformedLineDiscardsTheRestOfTheFile(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[boot]\nsystemd=true\nthis is not a setting\n\n[interop]\nenabled=false\nappendWindowsPath=false\n"))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if !strings.Contains(r.Summary, "line 3 is malformed") {
		t.Errorf("the summary should name the line: %q", r.Summary)
	}
	// The cost is the point: two settings below it are not applying.
	if !strings.Contains(r.Summary, "2 setting(s) below it are being ignored") {
		t.Errorf("the summary should say what it cost: %q", r.Summary)
	}
	if !strings.Contains(r.Detail, "stops reading wsl.conf at the first line it cannot parse") {
		t.Errorf("the detail should explain why: %q", r.Detail)
	}
}

// A malformed line with nothing after it costs nothing extra, and should not
// claim it did.
func TestWslConfMalformedLastLineClaimsNoLostSettings(t *testing.T) {
	r := (WslConf{}).Run(envWithWslConf("[boot]\nsystemd=true\nthis is not a setting\n"))
	if r.Status != probe.Warn {
		t.Fatalf("status %s", r.Status)
	}
	if strings.Contains(r.Summary, "below it are being ignored") {
		t.Errorf("nothing follows it, so nothing was lost: %q", r.Summary)
	}
}

// Every key in the table has to be reachable by the lint, or a real setting
// would be reported as unknown.
func TestWslConfAcceptsEveryKeyInTheTable(t *testing.T) {
	table, err := data.LoadWslConfKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range table.Keys {
		value := "true"
		switch {
		case strings.HasPrefix(k.Type, "enum:"):
			value = k.EnumValues()[0]
		case k.Type == "int":
			value = "1"
		case k.Type == "path":
			value = "/x"
		case k.Type == "string":
			value = "x"
		}
		text := "[" + k.Section + "]\n" + k.Key + "=" + value + "\n"
		if r := (WslConf{}).Run(envWithWslConf(text)); r.Status != probe.OK {
			t.Errorf("%s.%s=%s was flagged: %s %q", k.Section, k.Key, value, r.Status, r.Summary)
		}
	}
}

// The table has to carry the keys that actually cause trouble, or the lint is
// checking a file it only half understands.
func TestWslConfTableHasTheKeysThatMatter(t *testing.T) {
	table, err := data.LoadWslConfKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"boot.systemd", "boot.command", "boot.initTimeout",
		"automount.enabled", "automount.root", "automount.cgroups",
		"network.generateResolvConf", "interop.appendWindowsPath",
		"user.default", "fileServer.enabled", "general.guiApplications",
	} {
		section, key, _ := strings.Cut(want, ".")
		if _, ok := table.Lookup(section, key); !ok {
			t.Errorf("%s is missing from the table", want)
		}
	}
}
