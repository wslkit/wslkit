package disk

import (
	"errors"
	"strings"
	"testing"
)

// A missing file is not an error: the defaults are a complete configuration.
func TestDefaultConfigIsComplete(t *testing.T) {
	c := DefaultConfig()
	if !c.CompactTrim {
		t.Error("trimming should be on by default: without it compaction reclaims almost nothing")
	}
	if c.CompactRestart {
		t.Error("restarting should be off by default")
	}
	if c.UnlockTimeoutSeconds != 90 {
		t.Errorf("unlock timeout %d, want 90", c.UnlockTimeoutSeconds)
	}
	for _, k := range ConfigKeys() {
		if _, ok := ConfigValue(c, k); !ok {
			t.Errorf("%q is listed as a setting but has no value", k)
		}
	}
}

// What the tool writes, it must read back unchanged. A Windows path is full of
// backslashes, so the escaping is not optional.
func TestConfigRoundTrips(t *testing.T) {
	in := Config{
		ScanDirs:             []string{`D:\WSL`, `E:\disks\vm, old`},
		CompactTrim:          false,
		CompactRestart:       true,
		UnlockTimeoutSeconds: 300,
	}
	text := RenderConfig(in)
	out, err := ParseConfig(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if strings.Join(out.ScanDirs, "|") != strings.Join(in.ScanDirs, "|") {
		t.Errorf("scan dirs: got %v, want %v\n%s", out.ScanDirs, in.ScanDirs, text)
	}
	if out.CompactTrim != in.CompactTrim || out.CompactRestart != in.CompactRestart {
		t.Errorf("booleans: got %+v", out)
	}
	if out.UnlockTimeoutSeconds != in.UnlockTimeoutSeconds {
		t.Errorf("timeout: got %d", out.UnlockTimeoutSeconds)
	}
}

func TestParseConfigEmptyIsTheDefaults(t *testing.T) {
	c, err := ParseConfig("")
	if err != nil {
		t.Fatal(err)
	}
	d := DefaultConfig()
	if c.CompactTrim != d.CompactTrim || c.CompactRestart != d.CompactRestart ||
		c.UnlockTimeoutSeconds != d.UnlockTimeoutSeconds || len(c.ScanDirs) != 0 {
		t.Errorf("got %+v, want %+v", c, d)
	}
}

func TestParseConfigAcceptsAHandWrittenFile(t *testing.T) {
	c, err := ParseConfig(`
# a comment
[compact]
trim = false

[wsl]
unlock_timeout_seconds = 120

[scan]
dirs = ["D:\\WSL"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.CompactTrim {
		t.Error("trim was not read")
	}
	if c.UnlockTimeoutSeconds != 120 {
		t.Errorf("timeout %d", c.UnlockTimeoutSeconds)
	}
	if len(c.ScanDirs) != 1 || c.ScanDirs[0] != `D:\WSL` {
		t.Errorf("scan dirs %v", c.ScanDirs)
	}
}

// A file written by a later version must still load, or an upgrade followed by
// a downgrade leaves the tool unable to start.
func TestParseConfigIgnoresSettingsItDoesNotKnow(t *testing.T) {
	c, err := ParseConfig("[compact]\ntrim = false\nsomething_new = 7\n\n[brand]\nnew = \"x\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.CompactTrim {
		t.Error("the setting it does know was not applied")
	}
}

func TestParseConfigReportsWhereItGaveUp(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"not a section", "[compact\n", "line 1"},
		{"no equals", "[compact]\ntrim\n", "line 2"},
		{"bad boolean", "[compact]\ntrim = yes\n", "true or false"},
		{"bad number", "[wsl]\nunlock_timeout_seconds = soon\n", "whole number"},
		{"unterminated list", "[scan]\ndirs = [\"D:\\\\WSL\"\n", "line 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseConfig(c.in)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
			if !errors.Is(err, ErrRefused) {
				t.Errorf("a bad config is a refusal, not a crash: %v", err)
			}
		})
	}
}

// Semicolons, not commas: a Windows path may contain a comma but never a
// semicolon.
func TestSetScanDirsSplitsOnSemicolons(t *testing.T) {
	c := DefaultConfig()
	if err := SetConfigValue(&c, KeyScanDirs, `D:\WSL; E:\vm, old ;`); err != nil {
		t.Fatal(err)
	}
	if len(c.ScanDirs) != 2 || c.ScanDirs[0] != `D:\WSL` || c.ScanDirs[1] != `E:\vm, old` {
		t.Fatalf("got %#v", c.ScanDirs)
	}
	// And an empty value clears the list.
	if err := SetConfigValue(&c, KeyScanDirs, ""); err != nil {
		t.Fatal(err)
	}
	if len(c.ScanDirs) != 0 {
		t.Errorf("got %v", c.ScanDirs)
	}
}

func TestSetRejectsWhatItCannotStore(t *testing.T) {
	c := DefaultConfig()
	for _, tc := range []struct{ key, value, want string }{
		{KeyCompactTrim, "1", "true or false"},
		{KeyCompactTrim, "yes", "true or false"},
		{KeyCompactRestart, "on", "true or false"},
		{KeyUnlockTimeout, "-1", "whole number"},
		{KeyUnlockTimeout, "9999", "at most 3600"},
		{"compact.nonsense", "x", "there is no setting"},
	} {
		err := SetConfigValue(&c, tc.key, tc.value)
		if err == nil {
			t.Errorf("%s = %q should have been refused", tc.key, tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s = %q: error %q does not mention %q", tc.key, tc.value, err, tc.want)
		}
	}
}

// The error for an unknown setting lists the ones that exist, so the user does
// not need a second command to find out what they should have typed.
func TestUnknownSettingListsTheRealOnes(t *testing.T) {
	err := UnknownSettingError("compact.trimm")
	for _, k := range ConfigKeys() {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("the error should list %q: %v", k, err)
		}
	}
}

func TestConfigValuesAreAlwaysStrings(t *testing.T) {
	c := DefaultConfig()
	o := ConfigJSON(`C:\x\config.toml`, c, nil)
	settings, ok := o["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings is %T", o["settings"])
	}
	for _, k := range ConfigKeys() {
		if _, isString := settings[k].(string); !isString {
			t.Errorf("%s is %T, want a string", k, settings[k])
		}
	}
	if _, ok := o["wslconfig"]; ok {
		t.Error("the .wslconfig block should be absent when there is nothing to show")
	}
}

func TestConfigJSONIncludesTheReadOnlyWslconfigValues(t *testing.T) {
	o := ConfigJSON(`C:\x`, DefaultConfig(), map[string]string{"wsl2.vhdSize": "512GB"})
	w, ok := o["wslconfig"].(map[string]any)
	if !ok {
		t.Fatalf("wslconfig is %T", o["wslconfig"])
	}
	if w["wsl2.vhdSize"] != "512GB" {
		t.Errorf("got %v", w)
	}
}
