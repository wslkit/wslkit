package wslver

import "testing"

func TestParseAndString(t *testing.T) {
	cases := []struct{ in, out string }{
		{"2.7.13.0", "2.7.13"},
		{"2.7.13", "2.7.13"},
		{" v2.4.13.0 ", "2.4.13"},
		{"10.0.19041.7663", "10.0.19041.7663"},
		{"2", "2.0.0"},
	}
	for _, c := range cases {
		v, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if v.String() != c.out {
			t.Errorf("%q -> %q, want %q", c.in, v.String(), c.out)
		}
	}
	for _, bad := range []string{"", "a.b", "1.2.3.4.5", "-1.0"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestCompare(t *testing.T) {
	a, b := MustParse("2.4.13"), MustParse("2.7.13")
	if Compare(a, b) != -1 || Compare(b, a) != 1 || Compare(a, a) != 0 {
		t.Fatal("compare asymmetry")
	}
	if !a.Less(b) || !b.AtLeast(a) || !a.AtLeast(a) {
		t.Fatal("Less/AtLeast")
	}
	if Compare(MustParse("2.7.13.0"), MustParse("2.7.13")) != 0 {
		t.Fatal("trailing zero should be equal")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"2.7.13.0", "", "v1", "1.2.3.4.5", "999999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := Parse(s)
		if err != nil {
			return
		}
		if _, err := Parse(v.String()); err != nil {
			t.Fatalf("round trip failed for %q -> %q", s, v.String())
		}
	})
}
