//go:build windows

package collect

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/winapi/console"
)

func collectPorts(ctx context.Context, e *env.Env, o Options) error {
	var firstErr error
	for _, p := range []struct {
		proto string
		dst   *env.Field[env.PortPool]
	}{{"tcp", &e.Net.TCPPorts}, {"udp", &e.Net.UDPPorts}} {
		src := "netsh int ipv4 show dynamicport " + p.proto + " / excludedportrange protocol=" + p.proto
		out, err := netsh(ctx, "int", "ipv4", "show", "dynamicport", p.proto)
		if err != nil {
			*p.dst = env.Fail[env.PortPool](kindOf(err), src, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		dyn, err := parseDynamicPorts(out)
		if err != nil {
			*p.dst = env.Fail[env.PortPool](env.ErrOther, src, err)
			continue
		}
		out, err = netsh(ctx, "int", "ipv4", "show", "excludedportrange", "protocol="+p.proto)
		if err != nil {
			*p.dst = env.Fail[env.PortPool](kindOf(err), src, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		*p.dst = env.Ok(env.PortPool{Dynamic: dyn, Excluded: parseExcludedPorts(out)}, src)
	}
	return firstErr
}

func netsh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "netsh.exe", args...)
	console.OwnConsole(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return out.String(), nil
}
