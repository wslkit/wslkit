//go:build windows

// Package hvsock listens on Hyper-V sockets bound to a specific VM, using the
// Linux VSOCK template service id (Data1 = port), the scheme WSL itself uses.
// Works from an unelevated process without any GuestCommunicationServices
// registration (measured; see docs/research/2026-09-subcommands.md §0).
package hvsock

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

// Listen binds (vmid, port). Wildcard and "children" VM ids do not receive
// guest connections; the exact id is required.
func Listen(vmid string, port uint32) (net.Listener, error) {
	id, err := guid.FromString(vmid)
	if err != nil {
		return nil, fmt.Errorf("hvsock: bad vm id %q: %w", vmid, err)
	}
	l, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: id, ServiceID: winio.VsockServiceID(port)})
	if err != nil {
		return nil, fmt.Errorf("hvsock: listen %s:%d: %w", vmid, port, err)
	}
	return l, nil
}
