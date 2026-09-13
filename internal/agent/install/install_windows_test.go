//go:build windows

package install

import (
	"strings"
	"testing"
)

func TestUnit(t *testing.T) {
	u := Unit(`My Distro`)
	for _, want := range []string{"ExecStart=/usr/local/lib/wslkit/wslkit-agent --name \"My Distro\"", "Restart=always", "WantedBy=multi-user.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}
