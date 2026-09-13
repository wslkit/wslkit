// Package guest is the Linux side of the wslkit agent: it connects to the
// Windows daemon, serves the configured AF_UNIX listeners by opening streams
// to their Windows targets, and answers host-initiated opens for unix targets.
// The transport is injected (AF_VSOCK in production, net.Pipe/TCP in tests).
package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/mux"
	"github.com/wslkit/wslkit/internal/agent/proto"
	"github.com/wslkit/wslkit/internal/agent/relay"
	"github.com/wslkit/wslkit/internal/agent/target"
)

// Transport connects to the host; called again after every disconnect.
type Transport func(ctx context.Context) (io.ReadWriteCloser, error)

// Agent runs one guest.
type Agent struct {
	Config  config.Guest
	Name    string // distro name announced in HELLO
	Version string
	Connect Transport
	Logger  *log.Logger
	// ListenUnix lets tests replace socket creation (defaults to listenUnix).
	ListenUnix func(l config.Listener) (net.Listener, error)
	// Backoff between reconnects (default 2s, doubling to 30s).
	MinBackoff, MaxBackoff time.Duration
	// ConnectGrace is how long a local connection waits for the host session
	// when the agent is between connections (default 3s). Zero refuses at once.
	ConnectGrace *time.Duration
}

const defaultConnectGrace = 3 * time.Second

func (a *Agent) connectGrace() time.Duration {
	if a.ConnectGrace == nil {
		return defaultConnectGrace
	}
	return *a.ConnectGrace
}

// Run serves until ctx ends, reconnecting to the host as needed. Listeners are
// created once; connections arriving while the host is away are refused.
func (a *Agent) Run(ctx context.Context) error {
	if a.Logger == nil {
		a.Logger = log.New(os.Stderr, "wslkit-agent: ", log.LstdFlags)
	}
	if a.ListenUnix == nil {
		a.ListenUnix = listenUnix
	}
	if a.MinBackoff == 0 {
		a.MinBackoff = 2 * time.Second
	}
	if a.MaxBackoff == 0 {
		a.MaxBackoff = 30 * time.Second
	}

	var cur struct {
		sync.RWMutex
		s *mux.Session
	}
	current := func() *mux.Session { cur.RLock(); defer cur.RUnlock(); return cur.s }

	// Local listeners.
	for _, l := range a.Config.Listeners {
		ln, err := a.ListenUnix(l)
		if err != nil {
			return fmt.Errorf("listener %s: %w", l.Name, err)
		}
		go a.serveListener(ctx, ln, l, current)
		a.Logger.Printf("listening on %s for %s -> %s", l.Unix, l.Name, l.Target)
	}
	go func() {
		<-ctx.Done()
	}()

	backoff := a.MinBackoff
	for {
		conn, err := a.Connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.Logger.Printf("host not reachable: %v; retry in %s", err, backoff)
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = min(backoff*2, a.MaxBackoff)
			continue
		}
		sess, err := mux.Handshake(conn, proto.Hello{Role: proto.RoleGuest, Name: a.Name, AgentVersion: a.Version, Caps: []string{"sock"}}, 10*time.Second)
		if err != nil {
			a.Logger.Printf("handshake failed: %v", err)
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			continue
		}
		backoff = a.MinBackoff
		cur.Lock()
		cur.s = sess
		cur.Unlock()
		a.Logger.Printf("connected to host (wslkit %s)", sess.Peer().AgentVersion)
		a.serveSession(ctx, sess)
		cur.Lock()
		cur.s = nil
		cur.Unlock()
		a.Logger.Printf("host session ended: %v", sess.Err())
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// serveSession answers host-initiated opens (unix targets) until the session ends.
func (a *Agent) serveSession(ctx context.Context, sess *mux.Session) {
	for {
		req, err := sess.Accept(ctx)
		if err != nil {
			return
		}
		go func() {
			scheme, _, perr := config.ParseTarget(req.Target)
			if perr != nil || scheme != config.SchemeUnix {
				_ = req.Reject("guest only accepts unix: targets")
				return
			}
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			remote, err := target.Dial(dctx, req.Target)
			cancel()
			if err != nil {
				_ = req.Reject(err.Error())
				return
			}
			st, err := req.Accept()
			if err != nil {
				_ = remote.Close()
				return
			}
			_ = relay.Pipe(st, remote, nil, nil)
		}()
	}
}

// serveListener relays local connections to the listener's Windows target.
func (a *Agent) serveListener(ctx context.Context, ln net.Listener, l config.Listener, current func() *mux.Session) {
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go func() {
			// A client can arrive while the agent is reconnecting (the VM restarts,
			// the daemon is restarted). Wait briefly rather than failing the client.
			sess := current()
			for deadline := time.Now().Add(a.connectGrace()); sess == nil && time.Now().Before(deadline); {
				select {
				case <-time.After(25 * time.Millisecond):
				case <-ctx.Done():
					_ = c.Close()
					return
				}
				sess = current()
			}
			if sess == nil {
				a.Logger.Printf("%s: connection while host is disconnected; refusing", l.Name)
				_ = c.Close()
				return
			}
			octx, cancel := context.WithTimeout(ctx, 15*time.Second)
			st, err := sess.Open(octx, l.Target, map[string]string{"preset": l.Name})
			cancel()
			if err != nil {
				a.Logger.Printf("%s: open %s: %v", l.Name, l.Target, err)
				_ = c.Close()
				return
			}
			_ = relay.Pipe(target.Wrap(c), st, nil, nil)
		}()
	}
}

// listenUnix creates the socket, replacing a stale file, and applies owner/mode.
func listenUnix(l config.Listener) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(l.Unix), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(l.Unix); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(l.Unix)
	}
	ln, err := net.Listen("unix", l.Unix)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(l.Mode)
	if mode == 0 {
		mode = 0o600
	}
	if err := os.Chmod(l.Unix, mode); err != nil {
		_ = ln.Close()
		return nil, err
	}
	if l.OwnerUID >= 0 {
		if err := os.Chown(l.Unix, l.OwnerUID, -1); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("chown %s to uid %d: %w", l.Unix, l.OwnerUID, err)
		}
		// The parent directory must be traversable by the owner too.
		_ = os.Chown(filepath.Dir(l.Unix), l.OwnerUID, -1)
	}
	return ln, nil
}
