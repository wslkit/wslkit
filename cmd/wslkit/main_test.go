package main

import "testing"

func TestInvokedAs(t *testing.T) {
	cases := map[string]string{
		`C:\tools\wslkit.exe`:      "wslkit",
		`C:\tools\WslDoctor.EXE`:   "wsldoctor",
		`/usr/local/bin/wsldoctor`: "wsldoctor",
		`wslkit`:                   "wslkit",
	}
	for in, want := range cases {
		if got := invokedAs(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if aliases["wsldoctor"] != "doctor" {
		t.Fatal("wsldoctor alias must map to doctor")
	}
}
