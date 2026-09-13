package guest

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/mux"
	"github.com/wslkit/wslkit/internal/agent/proto"
)

// fakeHost accepts guest sessions over TCP and echoes uppercase on every
// accepted stream; it refuses target "tcp:refuse".
func fakeHost(t *testing.T) (addr string, sessions chan *mux.Session) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	sessions = make(chan *mux.Session, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s, err := mux.Handshake(c, proto.Hello{Role: proto.RoleHost, Name: "windows", AgentVersion: "t"}, 5*time.Second)
			if err != nil {
				continue
			}
			sessions <- s
			go func() {
				for {
					req, err := s.Accept(context.Background())
					if err != nil {
						return
					}
					if req.Target == "tcp:refuse:1" {
						_ = req.Reject("nope")
						continue
					}
					st, err := req.Accept()
					if err != nil {
						return
					}
					go func() {
						data, _ := io.ReadAll(st)
						_, _ = st.Write(bytes.ToUpper(data))
						_ = st.CloseWrite()
						_ = st.Close()
					}()
				}
			}()
		}
	}()
	return l.Addr().String(), sessions
}

func tcpTransport(addr string) Transport {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

// localListener replaces AF_UNIX with a loopback TCP listener so the test runs
// on every OS. The address is published through a channel because the agent
// creates the listener on its own goroutine.
func localListener(t *testing.T) (func(config.Listener) (net.Listener, error), func() string) {
	t.Helper()
	addrs := make(chan string, 1)
	return func(config.Listener) (net.Listener, error) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err == nil {
				select {
				case addrs <- l.Addr().String():
				default:
				}
			}
			return l, err
		}, func() string {
			select {
			case a := <-addrs:
				addrs <- a // keep it available for later calls
				return a
			case <-time.After(5 * time.Second):
				t.Fatal("listener was never created")
				return ""
			}
		}
}

func TestAgentRelaysLocalConnectionToHostTarget(t *testing.T) {
	hostAddr, sessions := fakeHost(t)
	listen, localAddr := localListener(t)
	a := &Agent{
		Config:     config.Guest{Port: 1, Listeners: []config.Listener{{Name: "echo", Unix: "/x", Target: "tcp:127.0.0.1:9", OwnerUID: -1}}},
		Name:       "TestDistro",
		Version:    "t",
		Connect:    tcpTransport(hostAddr),
		Logger:     log.New(io.Discard, "", 0),
		ListenUnix: listen,
		MinBackoff: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()
	select {
	case s := <-sessions:
		if s.Peer().Name != "TestDistro" {
			t.Fatalf("hello name %q", s.Peer().Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest never connected")
	}
	c, err := net.Dial("tcp", localAddr())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("through the agent"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "THROUGH THE AGENT" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestAgentReconnectsAfterHostDrop(t *testing.T) {
	hostAddr, sessions := fakeHost(t)
	listen, _ := localListener(t)
	a := &Agent{
		Config: config.Guest{Port: 1}, Name: "D", Version: "t",
		Connect: tcpTransport(hostAddr), Logger: log.New(io.Discard, "", 0), ListenUnix: listen,
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()
	first := <-sessions
	_ = first.Close() // host drops the session
	select {
	case second := <-sessions:
		if second == first {
			t.Fatal("same session")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest did not reconnect")
	}
}

func TestAgentRefusesLocalConnectionWhileDisconnected(t *testing.T) {
	// Host address that nobody listens on: connect fails, agent keeps retrying.
	listen, localAddr := localListener(t)
	noGrace := time.Duration(0)
	a := &Agent{
		Config: config.Guest{Port: 1, Listeners: []config.Listener{{Name: "x", Unix: "/x", Target: "tcp:127.0.0.1:9", OwnerUID: -1}}},
		Name:   "D", Version: "t",
		Connect: tcpTransport("127.0.0.1:1"), Logger: log.New(io.Discard, "", 0), ListenUnix: listen,
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, ConnectGrace: &noGrace,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	c, err := net.Dial("tcp", localAddr())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected the agent to close the connection, got %v", err)
	}
}

func TestListenUnixAppliesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("AF_UNIX modes are a Linux concern")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")
	// stale file in the way
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	ln, err := listenUnix(config.Listener{Unix: path, OwnerUID: -1, Mode: 0o660})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o660 {
		t.Fatalf("mode %v err %v", fi.Mode(), err)
	}
	// Re-listen replaces the stale socket.
	_ = ln.Close()
	ln2, err := listenUnix(config.Listener{Unix: path, OwnerUID: -1})
	if err != nil {
		t.Fatal(err)
	}
	_ = ln2.Close()
}
