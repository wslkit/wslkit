//go:build windows

package sock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wslkit/wslkit/internal/agent/config"
)

// Discover resolves the Windows-side target for a preset. A named pipe that
// does not exist yet is only a warning: the service may start later and the
// bridge picks it up. A gpg-agent socket file that does not exist is an error,
// because its port and nonce cannot be resolved without it.
func Discover(p Preset) (target string, warn string, err error) {
	if p.Target != "" {
		if strings.HasPrefix(p.Target, config.SchemeNPipe+":") {
			warn = checkPipe(strings.TrimPrefix(p.Target, config.SchemeNPipe+":"))
		}
		return p.Target, warn, nil
	}
	t, err := discoverAssuan(p)
	return t, "", err
}

func discoverAssuan(p Preset) (string, error) {
	switch p.Name {
	case "gpg-agent":
		return assuanTarget("S.gpg-agent")
	case "gpg-agent-ssh":
		return assuanTarget("S.gpg-agent.ssh")
	case "gpg-agent-extra":
		return assuanTarget("S.gpg-agent.extra")
	}
	return "", fmt.Errorf("preset %s has no Windows target and no discovery rule", p.Name)
}

// GnupgHome is %APPDATA%\gnupg unless GNUPGHOME overrides it.
func GnupgHome() string {
	if h := os.Getenv("GNUPGHOME"); h != "" {
		return h
	}
	return filepath.Join(os.Getenv("APPDATA"), "gnupg")
}

func assuanTarget(name string) (string, error) {
	path := filepath.Join(GnupgHome(), name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s does not exist. Start the Windows agent (gpg-connect-agent /bye) and, for the SSH socket, enable enable-win32-openssh-support in gpg-agent.conf", path)
	}
	return config.SchemeAssuan + ":" + path, nil
}

// checkPipe returns an advisory message when a named pipe does not exist yet.
func checkPipe(name string) string {
	base := strings.TrimPrefix(strings.ToLower(name), `\\.\pipe\`)
	entries, err := os.ReadDir(`\\.\pipe\`)
	if err != nil {
		return "" // cannot enumerate: let the daemon try at connect time
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), base) {
			return ""
		}
	}
	return fmt.Sprintf("%s does not exist yet. Start the Windows agent (OpenSSH Authentication Agent service, or 1Password) and it will work without re-running this command; until then connections fail.", name)
}
