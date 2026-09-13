package mux

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wslkit/wslkit/internal/agent/proto"
)

func pair(t *testing.T) (host, guest *Session) {
	t.Helper()
	a, b := net.Pipe()
	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		host, errA = Handshake(a, proto.Hello{Role: proto.RoleHost, Name: "windows"}, 2*time.Second)
	}()
	go func() {
		defer wg.Done()
		guest, errB = Handshake(b, proto.Hello{Role: proto.RoleGuest, Name: "Ubuntu"}, 2*time.Second)
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("handshake: %v %v", errA, errB)
	}
	t.Cleanup(func() { _ = host.Close(); _ = guest.Close() })
	return host, guest
}

func TestHandshakeAndPeer(t *testing.T) {
	host, guest := pair(t)
	if host.Peer().Name != "Ubuntu" || guest.Peer().Name != "windows" {
		t.Fatal("peer names")
	}
	if !guest.odd || host.odd {
		t.Fatal("parity")
	}
}

func TestHandshakeRejectsSameRole(t *testing.T) {
	a, b := net.Pipe()
	go func() { _, _ = Handshake(b, proto.Hello{Role: proto.RoleHost}, time.Second) }()
	if _, err := Handshake(a, proto.Hello{Role: proto.RoleHost}, time.Second); err == nil {
		t.Fatal("same role accepted")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	a, _ := net.Pipe()
	if _, err := Handshake(a, proto.Hello{Role: proto.RoleHost}, 100*time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
}

// echo accepts opens on s and echoes data until EOF, then half-closes.
func echo(t *testing.T, s *Session) {
	t.Helper()
	go func() {
		for {
			req, err := s.Accept(context.Background())
			if err != nil {
				return
			}
			if req.Target == "reject-me" {
				_ = req.Reject("not allowed")
				continue
			}
			st, err := req.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(st, st)
				_ = st.CloseWrite()
			}()
		}
	}()
}

func TestOpenEchoAndHalfClose(t *testing.T) {
	host, guest := pair(t)
	echo(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := guest.Open(ctx, `npipe:\\.\pipe\x`, map[string]string{"preset": "test"})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 3*proto.MaxPayload+123) // forces chunking
	_, _ = rand.Read(payload)
	go func() {
		_, _ = st.Write(payload)
		_ = st.CloseWrite()
	}()
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: %d vs %d bytes", len(got), len(payload))
	}
	if _, err := st.Write([]byte("x")); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("write after CloseWrite: %v", err)
	}
	_ = st.Close()
}

func TestOpenRejected(t *testing.T) {
	host, guest := pair(t)
	echo(t, host)
	_, err := guest.Open(context.Background(), "reject-me", nil)
	if err == nil || err.Error() != "mux: open reject-me: not allowed" {
		t.Fatalf("err = %v", err)
	}
}

func TestBothSidesOpenConcurrently(t *testing.T) {
	host, guest := pair(t)
	echo(t, host)
	echo(t, guest)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			roundtrip(t, guest, i)
		}(i)
		go func(i int) {
			defer wg.Done()
			roundtrip(t, host, i)
		}(i)
	}
	wg.Wait()
}

func roundtrip(t *testing.T, s *Session, i int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := s.Open(ctx, "echo", nil)
	if err != nil {
		t.Error(err)
		return
	}
	msg := bytes.Repeat([]byte{byte(i)}, 1000+i)
	if _, err := st.Write(msg); err != nil {
		t.Error(err)
		return
	}
	_ = st.CloseWrite()
	got, err := io.ReadAll(st)
	if err != nil || !bytes.Equal(got, msg) {
		t.Errorf("stream %d: %v len %d", st.ID(), err, len(got))
	}
	_ = st.Close()
}

func TestSessionCloseFailsStreams(t *testing.T) {
	host, guest := pair(t)
	echo(t, host)
	st, err := guest.Open(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = guest.Close()
	if _, err := st.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after session close should fail")
	}
	if _, err := guest.Open(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("open after close: %v", err)
	}
	<-host.Done()
	if host.Err() == nil {
		t.Fatal("host should observe the disconnect")
	}
}

func TestPing(t *testing.T) {
	host, _ := pair(t)
	if err := host.Ping(); err != nil {
		t.Fatal(err)
	}
}
