package data

import (
	"testing"

	"github.com/wslkit/wsldoctor/internal/wslver"
)

func TestLoadCompat(t *testing.T) {
	c, err := LoadCompat()
	if err != nil {
		t.Fatal(err)
	}
	if c.LatestStable == "" {
		t.Fatal("latest_stable empty")
	}
	d := c.Lookup("Ubuntu", "26.04")
	if d == nil || d.KnownBadMax != "2.4.13" {
		t.Fatalf("lookup ubuntu 26.04 = %+v", d)
	}
	if c.Lookup("ubuntu", "26.04.1") == nil {
		t.Error("prefix match should find 26.04 for 26.04.1")
	}
	if c.Lookup("alpine", "3.24.1") != nil {
		t.Error("unexpected alpine row")
	}
}

func TestMinimumRuntimeFromCapabilities(t *testing.T) {
	c, err := LoadCompat()
	if err != nil {
		t.Fatal(err)
	}
	d := c.Lookup("ubuntu", "26.04")
	min, reqs := c.MinimumRuntime(d)
	if min != wslver.MustParse("2.5.7") {
		t.Fatalf("min = %s, want 2.5.7", min)
	}
	if len(reqs) != 1 || reqs[0].Capability != "cgroup_v2" || reqs[0].Title == "" {
		t.Fatalf("reqs = %+v", reqs)
	}
}

func TestMinimumRuntimeCombinesExplicitAndCapabilities(t *testing.T) {
	c := &Compat{
		Capabilities: map[string]Capability{
			"a": {Since: "2.5.7", Title: "a"},
			"b": {Since: "2.6.2", Title: "b"},
		},
	}
	d := &DistroCompat{Requires: []string{"a", "b"}, MinRuntime: "2.4.4"}
	min, reqs := c.MinimumRuntime(d)
	if min != wslver.MustParse("2.6.2") {
		t.Fatalf("min = %s, want the highest (2.6.2)", min)
	}
	if reqs[0].Capability != "b" || len(reqs) != 3 {
		t.Fatalf("binding requirement should sort first: %+v", reqs)
	}
	if m, r := c.MinimumRuntime(&DistroCompat{KnownBadMax: "2.4.13"}); !m.IsZero() || r != nil {
		t.Fatalf("row with only observations must have no minimum, got %s %+v", m, r)
	}
}

func TestValidateRejectsUnknownCapability(t *testing.T) {
	c := &Compat{
		Schema: "wsldoctor/compat/v1", LatestStable: "2.7.14", LatestPrerelease: "2.9.11",
		ModernFormatMin: "2.4.4", ModernFormatRecommended: "2.4.8",
		Distros: []DistroCompat{{Flavor: "x", OsVersion: "1", Requires: []string{"nope"}}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unknown capability")
	}
	c.Capabilities = map[string]Capability{"nope": {Since: "bad", Title: "t"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unparsable since")
	}
	c.Capabilities["nope"] = Capability{Since: "2.5.0", Title: "t"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
