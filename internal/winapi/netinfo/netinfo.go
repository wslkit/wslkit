//go:build windows

// Package netinfo enumerates network adapters through GetAdaptersAddresses,
// classifying them the way WSL's own networking code does.
package netinfo

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IF_TYPE_PROP_VIRTUAL is not defined by x/sys; WSL treats it as a VPN adapter.
const ifTypePropVirtual = 53

type Adapter struct {
	Name        string
	Description string
	IfType      uint32
	Up          bool
	VPN         bool
	Loopback    bool
	Tunnel      bool
	DNSServers  []string
	DNSSuffix   string
	HasIPv4     bool
	HasIPv6     bool
}

// IsVPNType mirrors wsl::core::networking::IsInterfaceTypeVpn.
func IsVPNType(t uint32) bool {
	return t == windows.IF_TYPE_PPP || t == ifTypePropVirtual
}

// Adapters lists all adapters (both address families) with unicast and DNS info.
func Adapters() ([]Adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_PREFIX | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	size := uint32(16 * 1024)
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, fmt.Errorf("GetAdaptersAddresses: %w", err)
		}
	}
	var out []Adapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		a := Adapter{
			Name:        windows.UTF16PtrToString(aa.FriendlyName),
			Description: windows.UTF16PtrToString(aa.Description),
			IfType:      aa.IfType,
			Up:          aa.OperStatus == windows.IfOperStatusUp,
			VPN:         IsVPNType(aa.IfType),
			Loopback:    aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK,
			Tunnel:      aa.IfType == windows.IF_TYPE_TUNNEL,
			DNSSuffix:   windows.UTF16PtrToString(aa.DnsSuffix),
		}
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			if ip := u.Address.IP(); ip != nil {
				if ip.To4() != nil {
					a.HasIPv4 = true
				} else {
					a.HasIPv6 = true
				}
			}
		}
		for d := aa.FirstDnsServerAddress; d != nil; d = d.Next {
			if ip := d.Address.IP(); ip != nil {
				a.DNSServers = append(a.DNSServers, ip.String())
			}
		}
		out = append(out, a)
	}
	return out, nil
}
