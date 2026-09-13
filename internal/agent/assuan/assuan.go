// Package assuan reads gpg4win's Windows "socket" files: a text port number,
// a newline, then a 16-byte nonce that must be sent first after connecting to
// 127.0.0.1:<port>. Pure Go.
package assuan

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Endpoint is the decoded content of a gpg-agent socket file.
type Endpoint struct {
	Port  int
	Nonce [16]byte
}

// Parse decodes the file content.
func Parse(b []byte) (Endpoint, error) {
	var e Endpoint
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return e, errors.New("assuan: no newline after port")
	}
	port, err := strconv.Atoi(string(bytes.TrimRight(b[:i], "\r")))
	if err != nil || port <= 0 || port > 65535 {
		return e, fmt.Errorf("assuan: bad port %q", b[:i])
	}
	rest := b[i+1:]
	if len(rest) != 16 {
		return e, fmt.Errorf("assuan: nonce is %d bytes, want 16", len(rest))
	}
	e.Port = port
	copy(e.Nonce[:], rest)
	return e, nil
}

// ReadFile parses a socket file on disk.
func ReadFile(path string) (Endpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Endpoint{}, err
	}
	return Parse(b)
}
