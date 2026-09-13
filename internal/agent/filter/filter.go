// Package filter holds traffic filters applied by the Windows daemon to a
// relayed stream. Pure Go.
package filter

import (
	"encoding/binary"
	"io"

	"github.com/wslkit/wslkit/internal/agent/relay"
)

// New returns the filter pair for a name: toTarget filters guest->Windows
// traffic, fromTarget the reverse. Unknown names return nil filters.
func New(name string) (toTarget, fromTarget relay.Filter, ok bool) {
	switch name {
	case "ssh-agent":
		return &SSHAgent{}, nil, true
	case "":
		return nil, nil, true
	}
	return nil, nil, false
}

// SSH agent protocol constants (draft-miller-ssh-agent).
const (
	sshAgentcExtension = 27 // SSH_AGENTC_EXTENSION, sent by OpenSSH 8.9+ clients
	sshAgentFailure    = 5  // SSH_AGENT_FAILURE
	maxAgentMessage    = 256 * 1024
)

// SSHAgent answers SSH_AGENTC_EXTENSION requests locally with
// SSH_AGENT_FAILURE. Older Windows OpenSSH agents drop the connection on the
// unknown message, which breaks every OpenSSH 8.9+ client; answering
// "unsupported" makes the client fall back. Everything else is forwarded
// unchanged. Messages are re-framed across chunk boundaries.
type SSHAgent struct {
	buf []byte
}

// Forward implements relay.Filter.
func (f *SSHAgent) Forward(data []byte, replyTo io.Writer) ([]byte, error) {
	f.buf = append(f.buf, data...)
	var out []byte
	for {
		if len(f.buf) < 4 {
			return out, nil
		}
		n := binary.BigEndian.Uint32(f.buf[:4])
		if n == 0 || n > maxAgentMessage {
			// Not agent protocol; pass everything through untouched from here on.
			out = append(out, f.buf...)
			f.buf = nil
			return out, nil
		}
		if uint32(len(f.buf)-4) < n {
			return out, nil // wait for the rest of the message
		}
		msg := f.buf[:4+n]
		f.buf = f.buf[4+n:]
		if msg[4] == sshAgentcExtension {
			if _, err := replyTo.Write([]byte{0, 0, 0, 1, sshAgentFailure}); err != nil {
				return out, err
			}
			continue
		}
		out = append(out, msg...)
	}
}
