package filter

import (
	"bytes"
	"testing"
)

func msg(t byte, body ...byte) []byte {
	b := append([]byte{t}, body...)
	return append([]byte{0, 0, 0, byte(len(b))}, b...)
}

func TestSSHAgentAnswersExtensionsLocally(t *testing.T) {
	f := &SSHAgent{}
	var reply bytes.Buffer
	// request identities (11), then an extension (27), then sign (13), split at odd offsets
	stream := append(append(msg(11), msg(27, 's', 'e', 's', 's', 'i', 'o', 'n')...), msg(13, 1, 2, 3)...)
	var out []byte
	for i := 0; i < len(stream); i += 3 {
		end := i + 3
		if end > len(stream) {
			end = len(stream)
		}
		o, err := f.Forward(stream[i:end], &reply)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, o...)
	}
	want := append(msg(11), msg(13, 1, 2, 3)...)
	if !bytes.Equal(out, want) {
		t.Fatalf("forwarded %v want %v", out, want)
	}
	if !bytes.Equal(reply.Bytes(), []byte{0, 0, 0, 1, 5}) {
		t.Fatalf("reply %v", reply.Bytes())
	}
}

func TestSSHAgentPassesNonProtocolThrough(t *testing.T) {
	f := &SSHAgent{}
	var reply bytes.Buffer
	junk := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'x'}
	out, err := f.Forward(junk, &reply)
	if err != nil || !bytes.Equal(out, junk) || reply.Len() != 0 {
		t.Fatalf("%v %v %v", out, err, reply.Bytes())
	}
}

func TestNew(t *testing.T) {
	if a, b, ok := New("ssh-agent"); !ok || a == nil || b != nil {
		t.Fatal("ssh-agent")
	}
	if a, b, ok := New(""); !ok || a != nil || b != nil {
		t.Fatal("empty")
	}
	if _, _, ok := New("nope"); ok {
		t.Fatal("unknown accepted")
	}
}
