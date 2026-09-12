//go:build windows

// Package authenticode verifies a file's Authenticode signature with
// WinVerifyTrust, the same check official WSL builds apply to plugins before
// loading them. Works unelevated.
package authenticode

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWintrust        = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust = modWintrust.NewProc("WinVerifyTrust")
)

// WINTRUST_ACTION_GENERIC_VERIFY_V2 {00AAC56B-CD44-11d0-8CC2-00C04FC295EE}
var actionGenericVerifyV2 = windows.GUID{Data1: 0x00AAC56B, Data2: 0xCD44, Data3: 0x11d0, Data4: [8]byte{0x8C, 0xC2, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE}}

// wintrustFileInfo mirrors WINTRUST_FILE_INFO; the blank fields keep the ABI
// layout (hFile, pgKnownSubject) without being read from Go.
type wintrustFileInfo struct {
	cbStruct      uint32
	pcwszFilePath *uint16
	_             windows.Handle
	_             *windows.GUID
}

type wintrustData struct {
	cbStruct            uint32
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	pFile               *wintrustFileInfo
	dwStateAction       uint32
	hWVTStateData       windows.Handle
	pwszURLReference    *uint16
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

const (
	wtdUINone          = 2
	wtdRevokeNone      = 0
	wtdChoiceFile      = 1
	wtdStateVerify     = 1
	wtdStateClose      = 2
	wtdCacheOnlyURL    = 0x1000 // WTD_CACHE_ONLY_URL_RETRIEVAL: never hit the network
	wtdRevocationCheck = 0x10   // WTD_REVOCATION_CHECK_NONE
)

// Well-known HRESULTs.
const (
	TrustENoSignature       = 0x800B0100
	TrustEBadDigest         = 0x80096010
	CertEUntrustedRoot      = 0x800B0109
	TrustEExplicitDistr     = 0x800B0111 // TRUST_E_EXPLICIT_DISTRUST
	TrustESubjectNotTrusted = 0x800B0004
)

// Verify returns a short classification and the raw HRESULT (0 when trusted).
func Verify(path string) (string, uint32) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "error:path", 0xFFFFFFFF
	}
	fi := wintrustFileInfo{pcwszFilePath: p}
	fi.cbStruct = uint32(unsafe.Sizeof(fi))
	wd := wintrustData{
		dwUIChoice:          wtdUINone,
		fdwRevocationChecks: wtdRevokeNone,
		dwUnionChoice:       wtdChoiceFile,
		pFile:               &fi,
		dwStateAction:       wtdStateVerify,
		dwProvFlags:         wtdCacheOnlyURL | wtdRevocationCheck,
	}
	wd.cbStruct = uint32(unsafe.Sizeof(wd))
	r, _, _ := procWinVerifyTrust.Call(0, uintptr(unsafe.Pointer(&actionGenericVerifyV2)), uintptr(unsafe.Pointer(&wd)))
	hr := uint32(r)
	wd.dwStateAction = wtdStateClose
	procWinVerifyTrust.Call(0, uintptr(unsafe.Pointer(&actionGenericVerifyV2)), uintptr(unsafe.Pointer(&wd)))
	return Classify(hr), hr
}

// Classify maps a WinVerifyTrust HRESULT to the short names used in Env.
func Classify(hr uint32) string {
	switch hr {
	case 0:
		return "trusted"
	case TrustENoSignature:
		return "unsigned"
	case TrustEBadDigest:
		return "bad_digest"
	case CertEUntrustedRoot:
		return "untrusted_root"
	case TrustEExplicitDistr, TrustESubjectNotTrusted:
		return "distrusted"
	default:
		return fmt.Sprintf("error:0x%08X", hr)
	}
}
