package data

import "testing"

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
