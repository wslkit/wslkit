package assuan

import (
	"bytes"
	"testing"
)

func TestParse(t *testing.T) {
	nonce := bytes.Repeat([]byte{0xAB}, 16)
	e, err := Parse(append([]byte("54321\n"), nonce...))
	if err != nil || e.Port != 54321 || !bytes.Equal(e.Nonce[:], nonce) {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := Parse(append([]byte("54321\r\n"), nonce...)); err != nil {
		t.Fatal("CRLF should be tolerated:", err)
	}
	for _, bad := range [][]byte{[]byte("54321"), []byte("x\n" + string(nonce)), append([]byte("70000\n"), nonce...), []byte("1\nshort")} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
