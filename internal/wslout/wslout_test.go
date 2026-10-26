package wslout

import (
	"testing"
	"unicode/utf16"
)

func le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// The stderr of `wsl.exe -d Ubuntu -u root --exec /bin/sh -c 'echo "guest-err é"
// >&2; exit 3'` on WSL 3.0.3, byte for byte: wsl.exe's own warning in UTF-16LE,
// then the guest's line in UTF-8. Read as one encoding throughout, the guest's
// half came out as CJK characters in a disk move error.
func TestDecodeMixedStream(t *testing.T) {
	raw := []byte{
		0x77, 0x00, 0x73, 0x00, 0x6C, 0x00, 0x3A, 0x00, 0x20, 0x00, 0x44, 0x00, 0x4E, 0x00, 0x53, 0x00,
		0x20, 0x00, 0x54, 0x00, 0x75, 0x00, 0x6E, 0x00, 0x6E, 0x00, 0x65, 0x00, 0x6C, 0x00, 0x69, 0x00,
		0x6E, 0x00, 0x67, 0x00, 0x20, 0x00, 0x69, 0x00, 0x73, 0x00, 0x20, 0x00, 0x6E, 0x00, 0x6F, 0x00,
		0x74, 0x00, 0x20, 0x00, 0x73, 0x00, 0x75, 0x00, 0x70, 0x00, 0x70, 0x00, 0x6F, 0x00, 0x72, 0x00,
		0x74, 0x00, 0x65, 0x00, 0x64, 0x00, 0x0D, 0x00, 0x0A, 0x00,
		0x67, 0x75, 0x65, 0x73, 0x74, 0x2D, 0x65, 0x72, 0x72, 0x20, 0xC3, 0xA9, 0x0A,
	}
	want := "wsl: DNS Tunneling is not supported\r\nguest-err é\n"
	if got := Decode(raw); got != want {
		t.Errorf("Decode = %q, want %q", got, want)
	}
}

func TestDecode(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, ""},
		{"guest only", []byte("Ubuntu\nété\n"), "Ubuntu\nété\n"},
		{"wsl.exe only", le("Ubuntu\r\ndocker-desktop\r\n"), "Ubuntu\r\ndocker-desktop\r\n"},
		{"wsl.exe only, no newline", le("Ubuntu"), "Ubuntu"},
		// A localized message has no NUL in its characters, only in its
		// line ends. It must stay UTF-16 all the way through.
		{"wsl.exe in Chinese", le("默认分发\r\n上一个\r\n"), "默认分发\r\n上一个\r\n"},
		{"two wsl.exe lines then the guest", append(le("wsl: one\r\nwsl: two\r\n"), "from the guest\n"...), "wsl: one\r\nwsl: two\r\nfrom the guest\n"},
		{"odd trailing byte", append(le("ab"), 'c'), "ab"},
		{"trailing NULs", append(le("ab\r\n"), 0, 0), "ab\r\n"},
	} {
		if got := Decode(c.in); got != c.want {
			t.Errorf("%s: Decode = %q, want %q", c.name, got, c.want)
		}
	}
}
