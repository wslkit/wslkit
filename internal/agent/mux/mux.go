// Package mux multiplexes byte streams over one ordered connection using the
// wslkit agent protocol. Either side can open streams. Pure Go; tested with
// net.Pipe, used over Hyper-V sockets in production.
package mux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/wslkit/wslkit/internal/agent/proto"
)

// Per-stream inbound buffer, in frames. The reader loop blocks when a stream's
// buffer is full, which bounds memory at the cost of head-of-line blocking.
const streamBuffer = 64

var (
	ErrClosed       = errors.New("mux: session closed")
	ErrStreamClosed = errors.New("mux: stream closed")
)

// Session is one handshaken connection.
type Session struct {
	conn   io.ReadWriteCloser
	peer   proto.Hello
	odd    bool // this side allocates odd stream ids (guest)
	nextID uint32

	wmu     sync.Mutex // serialises writes
	smu     sync.Mutex
	streams map[uint32]*Stream
	pending map[uint32]chan proto.Frame // OPEN awaiting OPEN_OK / OPEN_ERR

	// Incoming opens are queued rather than handed over on a channel: the read
	// loop must never block on application progress, or two peers opening
	// streams at the same time deadlock each other.
	amu       sync.Mutex
	queue     []*OpenRequest
	signal    chan struct{}
	done      chan struct{}
	closeErr  error
	closeOnce sync.Once
}

// maxPendingOpens bounds the accept queue; a peer that floods us beyond this is
// refused rather than allowed to consume memory without limit.
const maxPendingOpens = 1024

// Handshake exchanges HELLO frames and returns a Session. ours.Role decides the
// stream id parity. deadline bounds the handshake only.
func Handshake(conn io.ReadWriteCloser, ours proto.Hello, deadline time.Duration) (*Session, error) {
	type res struct {
		peer proto.Hello
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		// Send and receive concurrently: on a synchronous transport (net.Pipe,
		// and hvsock with small buffers) both sides writing first would deadlock.
		werr := make(chan error, 1)
		go func() {
			hf, err := proto.EncodeHello(ours)
			if err == nil {
				err = proto.WriteFrame(conn, hf)
			}
			werr <- err
		}()
		f, rerr := proto.ReadFrame(conn)
		if err := <-werr; err != nil {
			ch <- res{err: fmt.Errorf("mux: sending hello: %w", err)}
			return
		}
		if rerr != nil {
			ch <- res{err: fmt.Errorf("mux: reading peer hello: %w", rerr)}
			return
		}
		peer, err := proto.DecodeHello(f)
		ch <- res{peer: peer, err: err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-time.After(deadline):
		_ = conn.Close()
		return nil, errors.New("mux: handshake timed out")
	}
	if r.err != nil {
		_ = conn.Close()
		return nil, r.err
	}
	if r.peer.Role == ours.Role {
		_ = conn.Close()
		return nil, fmt.Errorf("mux: both sides claim role %q", ours.Role)
	}
	s := &Session{
		conn:    conn,
		peer:    r.peer,
		odd:     ours.Role == proto.RoleGuest,
		streams: map[uint32]*Stream{},
		pending: map[uint32]chan proto.Frame{},
		signal:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	if s.odd {
		s.nextID = 1
	} else {
		s.nextID = 2
	}
	go s.readLoop()
	return s, nil
}

// Peer returns the peer's HELLO.
func (s *Session) Peer() proto.Hello { return s.peer }

// Done is closed when the session ends; Err then reports why.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns the reason the session ended (nil while running).
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.closeErr
	default:
		return nil
	}
}

// Close tears the session down; all streams fail.
func (s *Session) Close() error { s.shutdown(ErrClosed); return nil }

func (s *Session) shutdown(err error) {
	s.closeOnce.Do(func() {
		s.closeErr = err
		_ = s.conn.Close()
		s.smu.Lock()
		for _, st := range s.streams {
			st.fail(err)
		}
		for _, ch := range s.pending {
			close(ch)
		}
		s.smu.Unlock()
		close(s.done)
	})
}

func (s *Session) write(f proto.Frame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return proto.WriteFrame(s.conn, f)
}

// Open asks the peer to connect to target and returns the stream once accepted.
func (s *Session) Open(ctx context.Context, target string, meta map[string]string) (*Stream, error) {
	s.smu.Lock()
	if s.Err() != nil {
		s.smu.Unlock()
		return nil, ErrClosed
	}
	id := s.nextID
	s.nextID += 2
	reply := make(chan proto.Frame, 1)
	s.pending[id] = reply
	s.smu.Unlock()

	f, err := proto.EncodeOpen(id, proto.Open{Target: target, Meta: meta})
	if err != nil {
		return nil, err
	}
	if err := s.write(f); err != nil {
		s.dropPending(id)
		return nil, err
	}
	select {
	case r, ok := <-reply:
		if !ok {
			return nil, ErrClosed
		}
		if r.Type == proto.TypeOpenErr {
			return nil, fmt.Errorf("mux: open %s: %s", target, proto.DecodeOpenErr(r))
		}
		st := s.newStream(id, target)
		return st, nil
	case <-ctx.Done():
		s.dropPending(id)
		_ = s.write(proto.Frame{Type: proto.TypeClose, Stream: id})
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrClosed
	}
}

func (s *Session) dropPending(id uint32) {
	s.smu.Lock()
	delete(s.pending, id)
	s.smu.Unlock()
}

// OpenRequest is a peer's request to open a stream; call Accept or Reject.
type OpenRequest struct {
	Target string
	Meta   map[string]string
	id     uint32
	s      *Session
	once   sync.Once
}

// Accept acknowledges the open and returns the stream.
func (r *OpenRequest) Accept() (*Stream, error) {
	var st *Stream
	var err error
	r.once.Do(func() {
		st = r.s.newStream(r.id, r.Target)
		err = r.s.write(proto.Frame{Type: proto.TypeOpenOK, Stream: r.id})
		if err != nil {
			r.s.removeStream(r.id)
		}
	})
	if st == nil && err == nil {
		err = errors.New("mux: open request already answered")
	}
	return st, err
}

// Reject refuses the open with a message for the peer.
func (r *OpenRequest) Reject(msg string) error {
	var err error
	answered := false
	r.once.Do(func() {
		answered = true
		err = r.s.write(proto.EncodeOpenErr(r.id, msg))
	})
	if !answered {
		return errors.New("mux: open request already answered")
	}
	return err
}

// enqueue adds an open request without ever blocking the read loop. It returns
// false when the queue is full.
func (s *Session) enqueue(r *OpenRequest) bool {
	s.amu.Lock()
	if len(s.queue) >= maxPendingOpens {
		s.amu.Unlock()
		return false
	}
	s.queue = append(s.queue, r)
	s.amu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default: // a wake-up is already pending
	}
	return true
}

func (s *Session) dequeue() *OpenRequest {
	s.amu.Lock()
	defer s.amu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	r := s.queue[0]
	s.queue = s.queue[1:]
	if len(s.queue) > 0 {
		select {
		case s.signal <- struct{}{}:
		default:
		}
	}
	return r
}

// Accept blocks for the next open request from the peer.
func (s *Session) Accept(ctx context.Context) (*OpenRequest, error) {
	for {
		if r := s.dequeue(); r != nil {
			return r, nil
		}
		select {
		case <-s.signal:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.done:
			// Drain anything queued before the session ended.
			if r := s.dequeue(); r != nil {
				return r, nil
			}
			return nil, ErrClosed
		}
	}
}

func (s *Session) newStream(id uint32, target string) *Stream {
	st := &Stream{id: id, target: target, s: s, in: make(chan []byte, streamBuffer), closed: make(chan struct{})}
	s.smu.Lock()
	s.streams[id] = st
	s.smu.Unlock()
	return st
}

func (s *Session) removeStream(id uint32) {
	s.smu.Lock()
	delete(s.streams, id)
	s.smu.Unlock()
}

func (s *Session) lookup(id uint32) *Stream {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.streams[id]
}

func (s *Session) readLoop() {
	for {
		f, err := proto.ReadFrame(s.conn)
		if err != nil {
			s.shutdown(fmt.Errorf("mux: read: %w", err))
			return
		}
		switch f.Type {
		case proto.TypeOpen:
			o, err := proto.DecodeOpen(f)
			if err != nil {
				_ = s.write(proto.EncodeOpenErr(f.Stream, err.Error()))
				continue
			}
			req := &OpenRequest{Target: o.Target, Meta: o.Meta, id: f.Stream, s: s}
			if !s.enqueue(req) {
				_ = s.write(proto.EncodeOpenErr(f.Stream, "too many pending opens"))
			}
		case proto.TypeOpenOK, proto.TypeOpenErr:
			s.smu.Lock()
			ch := s.pending[f.Stream]
			delete(s.pending, f.Stream)
			s.smu.Unlock()
			if ch != nil {
				ch <- f
			}
		case proto.TypeData:
			if st := s.lookup(f.Stream); st != nil {
				st.deliver(f.Payload)
			}
		case proto.TypeClose:
			if st := s.lookup(f.Stream); st != nil {
				if f.Flags&proto.FlagCloseWrite != 0 {
					st.peerEOF()
				} else {
					st.fail(io.EOF)
					s.removeStream(f.Stream)
				}
			}
		case proto.TypePing:
			_ = s.write(proto.Frame{Type: proto.TypePong, Payload: f.Payload})
		case proto.TypePong:
			// heartbeat reply; nothing to do in v1
		default:
			s.shutdown(fmt.Errorf("mux: unknown frame type %d", f.Type))
			return
		}
	}
}

// Ping sends a heartbeat frame (the reply is consumed silently).
func (s *Session) Ping() error {
	return s.write(proto.Frame{Type: proto.TypePing, Payload: []byte{0, 0, 0, 0, 0, 0, 0, 1}})
}

// Stream is one multiplexed byte stream. It implements io.ReadWriteCloser plus
// CloseWrite for half-close, like a TCP connection.
type Stream struct {
	id     uint32
	target string
	s      *Session

	in      chan []byte
	pending []byte
	eof     bool
	closed  chan struct{}
	once    sync.Once
	errmu   sync.Mutex
	err     error
	wmu     sync.Mutex
	wclosed bool
}

// ID returns the stream id.
func (st *Stream) ID() uint32 { return st.id }

// Target returns the target named at open.
func (st *Stream) Target() string { return st.target }

func (st *Stream) deliver(b []byte) {
	select {
	case st.in <- b:
	case <-st.closed:
	}
}

func (st *Stream) peerEOF() { st.deliver(nil) }

func (st *Stream) fail(err error) {
	st.once.Do(func() {
		st.errmu.Lock()
		st.err = err
		st.errmu.Unlock()
		close(st.closed)
	})
}

// Read returns data from the peer; io.EOF after the peer half-closes.
func (st *Stream) Read(p []byte) (int, error) {
	for len(st.pending) == 0 {
		if st.eof {
			return 0, io.EOF
		}
		select {
		case b := <-st.in:
			if b == nil {
				st.eof = true
				return 0, io.EOF
			}
			st.pending = b
		case <-st.closed:
			// drain anything already queued before reporting the error
			select {
			case b := <-st.in:
				if b == nil {
					st.eof = true
					return 0, io.EOF
				}
				st.pending = b
				continue
			default:
			}
			st.errmu.Lock()
			err := st.err
			st.errmu.Unlock()
			if err == nil {
				err = ErrStreamClosed
			}
			return 0, err
		}
	}
	n := copy(p, st.pending)
	st.pending = st.pending[n:]
	return n, nil
}

// Write sends data to the peer in MaxPayload chunks.
func (st *Stream) Write(p []byte) (int, error) {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	if st.wclosed {
		return 0, ErrStreamClosed
	}
	select {
	case <-st.closed:
		return 0, ErrStreamClosed
	default:
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > proto.MaxPayload {
			n = proto.MaxPayload
		}
		if err := st.s.write(proto.Frame{Type: proto.TypeData, Stream: st.id, Payload: p[:n]}); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

// CloseWrite half-closes: the peer sees EOF, we can still read.
func (st *Stream) CloseWrite() error {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	if st.wclosed {
		return nil
	}
	st.wclosed = true
	return st.s.write(proto.Frame{Type: proto.TypeClose, Flags: proto.FlagCloseWrite, Stream: st.id})
}

// Close ends the stream in both directions and tells the peer.
func (st *Stream) Close() error {
	st.s.removeStream(st.id)
	err := st.s.write(proto.Frame{Type: proto.TypeClose, Stream: st.id})
	st.fail(ErrStreamClosed)
	return err
}

// Conn adapts a Stream to net.Conn for code that wants one.
type Conn struct {
	*Stream
}

func (c Conn) LocalAddr() net.Addr              { return addr("mux:" + c.target) }
func (c Conn) RemoteAddr() net.Addr             { return addr(c.target) }
func (c Conn) SetDeadline(time.Time) error      { return nil }
func (c Conn) SetReadDeadline(time.Time) error  { return nil }
func (c Conn) SetWriteDeadline(time.Time) error { return nil }

type addr string

func (a addr) Network() string { return "wslkit-mux" }
func (a addr) String() string  { return string(a) }
