// Package wslerr parses the error strings wsl.exe prints, such as
// "Error code: Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED",
// bare HRESULTs like 0x80370102, and the exit code 4294967295.
//
// Format (microsoft/WSL, src/windows/common/wslutil.cpp ErrorToString): every set
// bit of a context mask, in ascending bit order, joined by "/", then "/" and
// either a known error name or 0x%08x.
package wslerr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parsed is the decoded form of one error string.
type Parsed struct {
	Raw      string   // the substring that was recognised
	Segments []string // context path, e.g. ["Wsl","Service","CreateInstance"]; empty for a bare code
	Code     string   // "E_UNEXPECTED", "HCS_E_HYPERV_NOT_INSTALLED", "0x80370102", "4294967295"
	HRESULT  uint32   // parsed when Code is hex; 0 otherwise
	ExitCode bool     // Code was the decimal exit code 4294967295 (-1)
}

// Path returns the segments joined by "/", without the code.
func (p Parsed) Path() string { return strings.Join(p.Segments, "/") }

var (
	// A slash path ending in a code: Wsl/Service/E_UNEXPECTED or Wsl/InstallDistro/0x80070005
	rePath = regexp.MustCompile(`\b(Wsl(?:/[A-Za-z][A-Za-z0-9]*)*)/((?:0x[0-9A-Fa-f]{1,8})|[A-Z][A-Z0-9_]+)\b`)
	reHex  = regexp.MustCompile(`\b0[xX]([0-9A-Fa-f]{1,8})\b`)
	reName = regexp.MustCompile(`\b((?:WSL|HCS|HNS|E|ERROR|REGDB|CO|RPC|TYPE|DISP|CLASS|MK|OLE|STG|INPLACE|ENUM|VIEW|DATA|DV|DRAGDROP|CACHE|OLEOBJ|CLIENTSITE|CONVERT10|CLIPBRD|WSLC)_E_[A-Z0-9_]+|E_[A-Z0-9_]+)\b`)
	reExit = regexp.MustCompile(`\b4294967295\b`)
)

// Parse finds the first recognisable WSL error in s. It accepts full console
// output ("Catastrophic failure\nError code: Wsl/Service/E_UNEXPECTED"), a bare
// path, a bare HRESULT, a bare error name, or the exit code 4294967295.
func Parse(s string) (Parsed, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Parsed{}, fmt.Errorf("wslerr: empty input")
	}
	if m := rePath.FindStringSubmatch(s); m != nil {
		p := Parsed{Raw: m[0], Segments: strings.Split(m[1], "/"), Code: m[2]}
		if strings.HasPrefix(strings.ToLower(p.Code), "0x") {
			p.HRESULT = parseHex(p.Code[2:])
			p.Code = fmt.Sprintf("0x%08X", p.HRESULT)
		}
		return p, nil
	}
	if m := reHex.FindStringSubmatch(s); m != nil {
		h := parseHex(m[1])
		return Parsed{Raw: m[0], Code: fmt.Sprintf("0x%08X", h), HRESULT: h}, nil
	}
	if m := reName.FindStringSubmatch(s); m != nil {
		return Parsed{Raw: m[0], Code: m[1]}, nil
	}
	if m := reExit.FindString(s); m != "" {
		return Parsed{Raw: m, Code: m, HRESULT: 0xFFFFFFFF, ExitCode: true}, nil
	}
	return Parsed{}, fmt.Errorf("wslerr: no WSL error code found in %q", truncate(s, 80))
}

func parseHex(h string) uint32 {
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Win32Code extracts the Win32 error number from an HRESULT_FROM_WIN32 value
// (facility 7), or 0 when the HRESULT is not one.
func Win32Code(hr uint32) uint32 {
	if hr>>16 == 0x8007 {
		return hr & 0xFFFF
	}
	return 0
}
