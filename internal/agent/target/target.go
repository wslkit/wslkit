// Package target dials the endpoints named in OPEN frames. Parsing is pure;
// dialing is per-OS (target_windows.go dials named pipes, TCP and gpg4win
// Assuan sockets; target_linux.go dials AF_UNIX).
package target

import (
	"io"
	"net"

	"github.com/wslkit/wslkit/internal/agent/relay"
)

// conn adapts a net.Conn into relay.Duplex. Connections without a native
// half-close (named pipes) treat CloseWrite as a no-op; the peer sees EOF when
// the connection closes.
type conn struct {
	net.Conn
}

func (c conn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// Wrap adapts any net.Conn to relay.Duplex.
func Wrap(c net.Conn) relay.Duplex { return conn{c} }

var _ io.ReadWriteCloser = conn{}
