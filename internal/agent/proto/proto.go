// Package proto defines the wire format between the wslkit Windows daemon and
// the guest agent: a length-prefixed frame stream carrying a versioned
// handshake and multiplexed byte streams. Pure Go, no OS dependencies.
//
// Frame layout (big-endian):
//
//	type u8 | flags u8 | stream u32 | length u32 | payload[length]
//
// Stream ids are chosen by the opener: the guest uses odd ids, the host even
// ids, so both sides can open streams without coordination.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// Version is the protocol version both sides must agree on.
	Version = 1
	// Magic identifies a wslkit agent connection in the HELLO frame.
	Magic = "wslkit-agent"
	// MaxPayload bounds a single frame's payload.
	MaxPayload = 64 * 1024
	headerSize = 10
)

// Frame types.
const (
	TypeHello   uint8 = 1
	TypeOpen    uint8 = 2
	TypeOpenOK  uint8 = 3
	TypeOpenErr uint8 = 4
	TypeData    uint8 = 5
	TypeClose   uint8 = 6
	TypePing    uint8 = 7
	TypePong    uint8 = 8
)

// Close flags.
const (
	FlagCloseWrite uint8 = 1 // half-close: no more data from the sender; peer may still send
)

// Roles in the handshake.
const (
	RoleHost  = "host"
	RoleGuest = "guest"
)

// Frame is one unit on the wire.
type Frame struct {
	Type    uint8
	Flags   uint8
	Stream  uint32
	Payload []byte
}

// Hello is the JSON payload of TypeHello, sent by both sides first.
type Hello struct {
	Magic        string   `json:"magic"`
	Version      int      `json:"version"`
	Role         string   `json:"role"`
	Name         string   `json:"name"`          // distro name (guest) or "windows" (host)
	AgentVersion string   `json:"agent_version"` // wslkit version string
	Caps         []string `json:"caps,omitempty"`
}

// Open is the JSON payload of TypeOpen.
type Open struct {
	Target string            `json:"target"` // e.g. "npipe:\\.\pipe\openssh-ssh-agent", "unix:/run/x.sock"
	Meta   map[string]string `json:"meta,omitempty"`
}

// OpenErr is the JSON payload of TypeOpenErr.
type OpenErr struct {
	Error string `json:"error"`
}

var (
	ErrFrameTooLarge = errors.New("proto: frame payload exceeds MaxPayload")
	ErrBadMagic      = errors.New("proto: bad magic in hello")
	ErrVersion       = errors.New("proto: unsupported protocol version")
)

// WriteFrame encodes f onto w in one Write call.
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Payload) > MaxPayload {
		return ErrFrameTooLarge
	}
	buf := make([]byte, headerSize+len(f.Payload))
	buf[0] = f.Type
	buf[1] = f.Flags
	binary.BigEndian.PutUint32(buf[2:6], f.Stream)
	binary.BigEndian.PutUint32(buf[6:10], uint32(len(f.Payload)))
	copy(buf[headerSize:], f.Payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame decodes one frame from r.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[6:10])
	if n > MaxPayload {
		return Frame{}, ErrFrameTooLarge
	}
	f := Frame{Type: hdr[0], Flags: hdr[1], Stream: binary.BigEndian.Uint32(hdr[2:6])}
	if n > 0 {
		f.Payload = make([]byte, n)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}

// EncodeHello builds a HELLO frame.
func EncodeHello(h Hello) (Frame, error) {
	h.Magic, h.Version = Magic, Version
	b, err := json.Marshal(h)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Type: TypeHello, Payload: b}, nil
}

// DecodeHello validates and decodes a HELLO frame.
func DecodeHello(f Frame) (Hello, error) {
	var h Hello
	if f.Type != TypeHello {
		return h, fmt.Errorf("proto: expected hello, got frame type %d", f.Type)
	}
	if err := json.Unmarshal(f.Payload, &h); err != nil {
		return h, fmt.Errorf("proto: hello payload: %w", err)
	}
	if h.Magic != Magic {
		return h, ErrBadMagic
	}
	if h.Version != Version {
		return h, fmt.Errorf("%w: peer %d, ours %d", ErrVersion, h.Version, Version)
	}
	if h.Role != RoleHost && h.Role != RoleGuest {
		return h, fmt.Errorf("proto: unknown role %q", h.Role)
	}
	return h, nil
}

// EncodeOpen builds an OPEN frame for stream id.
func EncodeOpen(stream uint32, o Open) (Frame, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Type: TypeOpen, Stream: stream, Payload: b}, nil
}

// DecodeOpen decodes an OPEN payload.
func DecodeOpen(f Frame) (Open, error) {
	var o Open
	if err := json.Unmarshal(f.Payload, &o); err != nil {
		return o, fmt.Errorf("proto: open payload: %w", err)
	}
	if o.Target == "" {
		return o, errors.New("proto: open without target")
	}
	return o, nil
}

// EncodeOpenErr builds an OPEN_ERR frame.
func EncodeOpenErr(stream uint32, msg string) Frame {
	b, _ := json.Marshal(OpenErr{Error: msg})
	return Frame{Type: TypeOpenErr, Stream: stream, Payload: b}
}

// DecodeOpenErr extracts the error message (never fails: falls back to raw payload).
func DecodeOpenErr(f Frame) string {
	var e OpenErr
	if json.Unmarshal(f.Payload, &e) == nil && e.Error != "" {
		return e.Error
	}
	return string(f.Payload)
}
