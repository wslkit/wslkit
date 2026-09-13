//go:build linux

package guest

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// VsockTransport connects to the host (CID 2) on port over AF_VSOCK, the same
// path WSL's own init uses (UtilConnectVsock in src/linux/init/util.cpp).
func VsockTransport(port uint32) Transport {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("vsock socket: %w", err)
		}
		// Connect timeout via the vsock-specific option (microseconds in a timeval).
		// A connect to a port nobody listens on hangs until this timeout rather
		// than failing fast, so keep it short: the agent retries anyway.
		tv := unix.NsecToTimeval(int64(4 * time.Second))
		_ = unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &tv)
		if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("vsock connect to host port %d: %w", port, err)
		}
		f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d", port))
		return f, nil
	}
}
