//go:build windows

package vmid

import "testing"

func TestParseCommandLine(t *testing.T) {
	cases := map[string]string{
		`"C:\Program Files\WSL\wslhost.exe" --vm-id {440371B8-EC9A-4297-B404-C5A6DB08616D} --handle 1234`: "440371b8-ec9a-4297-b404-c5a6db08616d",
		`wslrelay.exe --mode 2 --vm-id 440371b8-ec9a-4297-b404-c5a6db08616d`:                              "440371b8-ec9a-4297-b404-c5a6db08616d",
		`wsl.exe -d Ubuntu`: "",
	}
	for in, want := range cases {
		got, ok := ParseCommandLine(in)
		if (want == "") == ok || got != want {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
}
