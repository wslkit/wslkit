//go:build windows

package target

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"

	"github.com/wslkit/wslkit/internal/agent/assuan"
	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/relay"
)

// Dial connects to a Windows-side target.
func Dial(ctx context.Context, target string) (relay.Duplex, error) {
	scheme, rest, err := config.ParseTarget(target)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case config.SchemeNPipe:
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		c, err := winio.DialPipeContext(dctx, rest)
		if err != nil {
			return nil, fmt.Errorf("named pipe %s: %w", rest, err)
		}
		return Wrap(c), nil
	case config.SchemeTCP:
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", rest)
		if err != nil {
			return nil, err
		}
		return Wrap(c), nil
	case config.SchemeAssuan:
		ep, err := assuan.ReadFile(rest)
		if err != nil {
			return nil, err
		}
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", ep.Port))
		if err != nil {
			return nil, fmt.Errorf("gpg-agent at 127.0.0.1:%d: %w", ep.Port, err)
		}
		if _, err := c.Write(ep.Nonce[:]); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("gpg-agent nonce: %w", err)
		}
		return Wrap(c), nil
	default:
		return nil, fmt.Errorf("target scheme %q cannot be dialed on Windows", scheme)
	}
}
