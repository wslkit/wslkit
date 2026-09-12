package wslerr

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		segs []string
		code string
		hr   uint32
		exit bool
	}{
		{"Wsl/Service/E_UNEXPECTED", []string{"Wsl", "Service"}, "E_UNEXPECTED", 0, false},
		{"Catastrophic failure\r\nError code: Wsl/Service/E_UNEXPECTED\r\n", []string{"Wsl", "Service"}, "E_UNEXPECTED", 0, false},
		{"Error code: Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED", []string{"Wsl", "Service", "CreateInstance", "CreateVm", "HCS"}, "HCS_E_HYPERV_NOT_INSTALLED", 0, false},
		{"Wsl/InstallDistro/0x80070005", []string{"Wsl", "InstallDistro"}, "0x80070005", 0x80070005, false},
		{"Error code: CreateInstance/CreateVm/ConfigureNetworking/0x8007054f", nil, "0x8007054F", 0x8007054f, false},
		{"0x80370102", nil, "0x80370102", 0x80370102, false},
		{"The virtual machine could not be started because a required feature is not installed. Error: 0x80370102", nil, "0x80370102", 0x80370102, false},
		{"HCS_E_SERVICE_NOT_AVAILABLE", nil, "HCS_E_SERVICE_NOT_AVAILABLE", 0, false},
		{"Wsl/WSL_E_DEFAULT_DISTRO_NOT_FOUND", []string{"Wsl"}, "WSL_E_DEFAULT_DISTRO_NOT_FOUND", 0, false},
		{"process exited with code 4294967295", nil, "4294967295", 0xFFFFFFFF, true},
	}
	for _, c := range cases {
		p, err := Parse(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(p.Segments, c.segs) || p.Code != c.code || p.HRESULT != c.hr || p.ExitCode != c.exit {
			t.Errorf("%q -> %+v", c.in, p)
		}
	}
	for _, bad := range []string{"", "hello world", "wsl is slow"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestWin32Code(t *testing.T) {
	if Win32Code(0x8007054f) != 0x54f || Win32Code(0x80370102) != 0 {
		t.Fatal("Win32Code")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"Wsl/Service/E_UNEXPECTED", "0x80370102", "4294967295", "", "Wsl/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := Parse(s)
		if err == nil && p.Code == "" {
			t.Fatal("parsed without a code")
		}
	})
}
