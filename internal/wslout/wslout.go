// Package wslout decodes what wsl.exe writes to a pipe.
package wslout

import (
	"bytes"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Decode turns whatever wsl.exe wrote into a Go string.
//
// One stream can hold two encodings. wsl.exe writes its own messages as
// UTF-16LE, and output from inside a distribution passes through as the UTF-8
// it was written in. Measured on WSL 3.0.3: a command that fails inside the
// guest gives stderr as a UTF-16LE warning line, "wsl: DNS Tunneling is not
// supported\r\n", followed by the guest's UTF-8. Decoding the whole stream one
// way garbles one half of it.
//
// A stream with no NUL in it is UTF-8. Otherwise it is read as UTF-16LE one
// line at a time, and once the rest has no NUL and is valid UTF-8 it is the
// guest's and read as that. The order is only ever that way round: wsl.exe has
// its say before the guest starts.
func Decode(b []byte) string {
	if !bytes.ContainsRune(b, 0) {
		return string(b)
	}
	var out strings.Builder
	for len(b) > 0 {
		if !bytes.ContainsRune(b, 0) && utf8.Valid(b) {
			out.Write(b)
			break
		}
		n := utf16Line(b)
		out.WriteString(decodeUTF16(b[:n]))
		b = b[n:]
	}
	return strings.TrimRight(out.String(), "\x00")
}

// utf16Line is the length of the first UTF-16LE line of b, newline included,
// or all of b when there is no newline.
func utf16Line(b []byte) int {
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == '\n' && b[i+1] == 0 {
			return i + 2
		}
	}
	return len(b)
}

func decodeUTF16(b []byte) string {
	// An odd byte at the end is half a character and cannot be decoded.
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}
