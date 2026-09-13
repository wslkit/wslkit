package proto

import (
	"bytes"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	frames := []Frame{
		{Type: TypeData, Stream: 7, Payload: []byte("hello")},
		{Type: TypeClose, Flags: FlagCloseWrite, Stream: 7},
		{Type: TypePing, Payload: make([]byte, 8)},
		{Type: TypeData, Stream: 0xFFFFFFFF, Payload: bytes.Repeat([]byte{0xAB}, MaxPayload)},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != want.Type || got.Flags != want.Flags || got.Stream != want.Stream || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("got %+v want %+v", got.Type, want.Type)
		}
	}
	if err := WriteFrame(&buf, Frame{Type: TypeData, Payload: make([]byte, MaxPayload+1)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize write: %v", err)
	}
	// Oversize length on the wire is rejected before allocation.
	hdr := []byte{TypeData, 0, 0, 0, 0, 1, 0xFF, 0xFF, 0xFF, 0xFF}
	if _, err := ReadFrame(bytes.NewReader(hdr)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize read: %v", err)
	}
}

func TestHello(t *testing.T) {
	f, err := EncodeHello(Hello{Role: RoleGuest, Name: "Ubuntu", AgentVersion: "1.2.3", Caps: []string{"sock"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := DecodeHello(f)
	if err != nil || h.Magic != Magic || h.Version != Version || h.Name != "Ubuntu" || h.Caps[0] != "sock" {
		t.Fatalf("%+v %v", h, err)
	}
	bad := f
	bad.Payload = []byte(`{"magic":"nope","version":1,"role":"guest"}`)
	if _, err := DecodeHello(bad); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("bad magic: %v", err)
	}
	bad.Payload = []byte(`{"magic":"wslkit-agent","version":99,"role":"guest"}`)
	if _, err := DecodeHello(bad); !errors.Is(err, ErrVersion) {
		t.Fatalf("bad version: %v", err)
	}
	bad.Payload = []byte(`{"magic":"wslkit-agent","version":1,"role":"alien"}`)
	if _, err := DecodeHello(bad); err == nil {
		t.Fatal("bad role accepted")
	}
	if _, err := DecodeHello(Frame{Type: TypeData}); err == nil {
		t.Fatal("non-hello accepted")
	}
}

func TestOpen(t *testing.T) {
	f, err := EncodeOpen(3, Open{Target: `npipe:\\.\pipe\x`, Meta: map[string]string{"preset": "ssh-agent"}})
	if err != nil {
		t.Fatal(err)
	}
	o, err := DecodeOpen(f)
	if err != nil || o.Target != `npipe:\\.\pipe\x` || o.Meta["preset"] != "ssh-agent" {
		t.Fatalf("%+v %v", o, err)
	}
	if _, err := DecodeOpen(Frame{Payload: []byte(`{}`)}); err == nil {
		t.Fatal("empty target accepted")
	}
	e := EncodeOpenErr(3, "denied")
	if DecodeOpenErr(e) != "denied" || e.Stream != 3 {
		t.Fatal("open err round trip")
	}
	if DecodeOpenErr(Frame{Payload: []byte("raw text")}) != "raw text" {
		t.Fatal("fallback")
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{TypeData, 0, 0, 0, 0, 1, 0, 0, 0, 3, 'a', 'b', 'c'})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadFrame(bytes.NewReader(data)) // must not panic
	})
}
