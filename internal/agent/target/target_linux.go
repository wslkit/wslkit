//go:build linux

package target

import (
	"context"
	"fmt"
	"net"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/relay"
)

// Dial connects to a guest-side target (AF_UNIX only).
func Dial(ctx context.Context, target string) (relay.Duplex, error) {
	scheme, rest, err := config.ParseTarget(target)
	if err != nil {
		return nil, err
	}
	if scheme != config.SchemeUnix {
		return nil, fmt.Errorf("target scheme %q cannot be dialed in the guest", scheme)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", rest)
	if err != nil {
		return nil, err
	}
	return Wrap(c), nil
}
