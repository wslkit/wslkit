package host

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/mux"
	"github.com/wslkit/wslkit/internal/agent/proto"
	"github.com/wslkit/wslkit/internal/agent/relay"
	"github.com/wslkit/wslkit/internal/agent/target"
)

// fakeDialer resolves "tcp:" targets to real loopback connections and refuses others.
func fakeDialer(ctx context.Context, t string) (relay.Duplex, error) {
	scheme, rest, err := config.ParseTarget(t)
	if err != nil {
		return nil, err
	}
	if scheme != config.SchemeTCP {
		return nil, context.DeadlineExceeded
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", rest)
	if err != nil {
		return nil, err
	}
	return target.Wrap(c), nil
}

// upperEcho serves a TCP listener that uppercases everything it receives.
func upperEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				data, _ := io.ReadAll(c)
				_, _ = c.Write(bytes.ToUpper(data))
				_ = c.Close()
			}()
		}
	}()
	return l.Addr().String()
}

func startDaemon(t *testing.T, cfg config.Host) (*Daemon, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{Config: cfg, Version: "test", Dial: fakeDialer, StatusFile: filepath.Join(t.TempDir(), "status.json")}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = d.Serve(ctx, l, "tcp-test") }()
	// Stop the daemon and wait for it before the temp dir is removed.
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return d, l.Addr().String()
}

func guestSession(t *testing.T, addr string) *mux.Session {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	s, err := mux.Handshake(c, proto.Hello{Role: proto.RoleGuest, Name: "TestDistro", AgentVersion: "test"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDaemonRelaysAllowedTarget(t *testing.T) {
	echoAddr := upperEcho(t)
	tgt := "tcp:" + echoAddr
	d, addr := startDaemon(t, config.Host{Port: 1, Allow: []string{tgt}})
	g := guestSession(t, addr)

	st, err := g.Open(context.Background(), tgt, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.Write([]byte("relay me"))
	_ = st.CloseWrite()
	got, err := io.ReadAll(st)
	if err != nil || string(got) != "RELAY ME" {
		t.Fatalf("got %q err %v", got, err)
	}
	_ = st.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		s := d.Snapshot()
		if len(s.Sessions) == 1 && s.Sessions[0].Distro == "TestDistro" && s.Sessions[0].Opened == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never reflected the session: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ss, ok, err := ReadStatusFrom(d.StatusFile); err != nil || !ok || ss.PID == 0 || ss.Listening != "tcp-test" {
		t.Fatalf("status file: %+v %v %v", ss, ok, err)
	}
}

func TestDaemonRefusesUnlistedTarget(t *testing.T) {
	echoAddr := upperEcho(t)
	d, addr := startDaemon(t, config.Host{Port: 1, Allow: []string{"tcp:127.0.0.1:1"}})
	g := guestSession(t, addr)
	_, err := g.Open(context.Background(), "tcp:"+echoAddr, nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v", err)
	}
	// Dial failure of an allowed target is reported to the guest too.
	_, err = g.Open(context.Background(), "tcp:127.0.0.1:1", nil)
	if err == nil {
		t.Fatal("dial failure should surface as open error")
	}
	if d.Snapshot().Sessions[0].Refused != 2 {
		t.Fatalf("refused count %+v", d.Snapshot())
	}
}

func TestDaemonRejectsBadHandshake(t *testing.T) {
	_, addr := startDaemon(t, config.Host{Port: 1})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	// A second "host" must be refused.
	if _, err := mux.Handshake(c, proto.Hello{Role: proto.RoleHost}, 3*time.Second); err == nil {
		t.Fatal("host-to-host handshake accepted")
	}
}

func TestStatusString(t *testing.T) {
	s := Status{PID: 1, Version: "v", Sessions: []SessionStatus{{Distro: "Ubuntu"}}}
	if out := s.String(); !strings.Contains(out, "Ubuntu") || !strings.Contains(out, "pid 1") {
		t.Fatalf("%q", out)
	}
}
