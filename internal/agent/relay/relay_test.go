package relay

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// tcpPair returns two connected TCP ends (net.Pipe lacks CloseWrite).
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	ch := make(chan *net.TCPConn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		ch <- c.(*net.TCPConn)
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c.(*net.TCPConn), <-ch
}

func TestPipeRelaysAndPropagatesHalfClose(t *testing.T) {
	clientSide, relayA := tcpPair(t) // client <-> relay end A
	relayB, serverSide := tcpPair(t) // relay end B <-> server
	done := make(chan error, 1)
	go func() { done <- Pipe(relayA, relayB, nil, nil) }()

	// server echoes uppercase and half-closes when the client half-closes
	go func() {
		data, _ := io.ReadAll(serverSide)
		_, _ = serverSide.Write(bytes.ToUpper(data))
		_ = serverSide.CloseWrite()
	}()
	if _, err := clientSide.Write([]byte("hello relay")); err != nil {
		t.Fatal(err)
	}
	_ = clientSide.CloseWrite()
	got, err := io.ReadAll(clientSide)
	if err != nil || string(got) != "HELLO RELAY" {
		t.Fatalf("got %q err %v", got, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not finish")
	}
}

type upper struct{}

func (upper) Forward(b []byte, _ io.Writer) ([]byte, error) { return bytes.ToUpper(b), nil }

type answerLocally struct{}

// answerLocally swallows anything starting with '?' and replies "!" to the sender.
func (answerLocally) Forward(b []byte, reply io.Writer) ([]byte, error) {
	if len(b) > 0 && b[0] == '?' {
		_, _ = reply.Write([]byte("!"))
		return nil, nil
	}
	return b, nil
}

func TestFilters(t *testing.T) {
	clientSide, relayA := tcpPair(t)
	relayB, serverSide := tcpPair(t)
	go func() { _ = Pipe(relayA, relayB, answerLocally{}, upper{}) }()
	go func() {
		data, _ := io.ReadAll(serverSide)
		_, _ = serverSide.Write(append([]byte("server saw: "), data...))
		_ = serverSide.CloseWrite()
	}()
	_, _ = clientSide.Write([]byte("?local"))
	buf := make([]byte, 1)
	if _, err := io.ReadFull(clientSide, buf); err != nil || buf[0] != '!' {
		t.Fatalf("local answer: %q %v", buf, err)
	}
	_, _ = clientSide.Write([]byte("forwarded"))
	_ = clientSide.CloseWrite()
	got, _ := io.ReadAll(clientSide)
	if string(got) != "SERVER SAW: FORWARDED" {
		t.Fatalf("got %q", got)
	}
}
