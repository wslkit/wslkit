// Package payload embeds the guest agent binaries built by tools/build-agent.
// The directory always contains README.md so the embed pattern matches even
// when no binary has been built; Binary reports whether one is present.
package payload

import (
	"embed"
	"fmt"
)

//go:embed all:files
var files embed.FS

// Binary returns the guest agent for a Linux architecture ("amd64", "arm64").
func Binary(arch string) ([]byte, error) {
	name := "files/wslkit-agent-linux-" + arch
	b, err := files.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("guest agent for linux/%s is not embedded in this build; run `go run ./tools/build-agent` before building wslkit", arch)
	}
	return b, nil
}

// Available lists the architectures with an embedded agent.
func Available() []string {
	var out []string
	for _, a := range []string{"amd64", "arm64"} {
		if _, err := files.ReadFile("files/wslkit-agent-linux-" + a); err == nil {
			out = append(out, a)
		}
	}
	return out
}
