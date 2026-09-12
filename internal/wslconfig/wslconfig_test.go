package wslconfig

import "testing"

const sample = `# global
[wsl2]
memory=4GB   # cap
processors = 2
networkingMode=mirrored
bogus line here

[Experimental]
autoMemoryReclaim="gradual"
[broken
`

func TestParse(t *testing.T) {
	c := Parse(sample)
	if v, ok := c.Get("wsl2", "memory"); !ok || v != "4GB" {
		t.Errorf("memory = %q %v", v, ok)
	}
	if v, ok := c.Get("WSL2", "Processors"); !ok || v != "2" {
		t.Errorf("processors = %q %v", v, ok)
	}
	if v, ok := c.Get("experimental", "autoMemoryReclaim"); !ok || v != "gradual" {
		t.Errorf("autoMemoryReclaim = %q %v", v, ok)
	}
	if len(c.Problems) != 2 {
		t.Errorf("problems = %+v", c.Problems)
	}
	if len(c.Sections) != 2 || c.Sections[1] != "experimental" {
		t.Errorf("sections = %v", c.Sections)
	}
	for _, e := range c.Entries {
		if e.Key == "memory" && e.Line != 3 {
			t.Errorf("memory line = %d", e.Line)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]uint64{"4GB": 4 << 30, "512MB": 512 << 20, "1024": 1024, "8gb": 8 << 30, "2TB": 2 << 40}
	for in, want := range cases {
		got, ok := ParseSize(in)
		if !ok || got != want {
			t.Errorf("%q -> %d %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "GB", "4.5GB", "x"} {
		if _, ok := ParseSize(bad); ok {
			t.Errorf("%q should fail", bad)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(sample)
	f.Fuzz(func(t *testing.T, s string) { _ = Parse(s) })
}
