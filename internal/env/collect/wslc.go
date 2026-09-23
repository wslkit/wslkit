package collect

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
)

// wslcPublicResolver is the resolver the result is compared with, to tell a
// broken resolver from no network at all.
const wslcPublicResolver = "1.1.1.1"

// wslcDNSScript runs inside a wslc session VM. For each IPv4 nameserver in
// its resolv.conf, which is what a container is given, it asks for one name
// and prints the DNS status the answer carried, or "timeout".
//
// Measured on WSL 2.9.12: the session VM's resolv.conf held the host's IPv4
// resolver and its IPv6 ones; a container got only the IPv4 one, and the
// query to it came back SERVFAIL without ever leaving the PC. The VM has dig.
const wslcDNSScript = `q() {
  s=$(dig +time=3 +tries=1 example.com "@$1" 2>&1 | sed -n 's/.*status: \([A-Z]*\).*/\1/p' | head -n 1)
  echo "$2 $1 ${s:-timeout}"
}
if ! command -v dig >/dev/null 2>&1; then echo "error dig is not in the session VM"; exit 0; fi
for ns in $(awk '$1 == "nameserver" && $2 ~ /^[0-9.]+$/ {print $2}' /etc/resolv.conf 2>/dev/null); do
  q "$ns" resolver
done
q ` + wslcPublicResolver + ` public
`

// parseWSLCSessions reads the session names out of `wslc info --format json`.
func parseWSLCSessions(out string) (version string, names []string, err error) {
	var info struct {
		Client struct {
			Version string `json:"Version"`
		} `json:"Client"`
		Server struct {
			Sessions []struct {
				Name string `json:"Name"`
			} `json:"Sessions"`
		} `json:"Server"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &info); err != nil {
		return "", nil, fmt.Errorf("wslc info printed something unexpected: %w", err)
	}
	for _, s := range info.Server.Sessions {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	return info.Client.Version, names, nil
}

// parseWSLCDNS reads what wslcDNSScript printed.
func parseWSLCDNS(name, out string) env.WSLCSession {
	s := env.WSLCSession{Name: name, Resolvers: []env.WSLCResolver{}}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "resolver", "public":
			if len(f) != 3 {
				continue
			}
			r := env.WSLCResolver{Addr: f[1], Status: f[2]}
			if f[0] == "public" {
				s.Public = r
			} else {
				s.Resolvers = append(s.Resolvers, r)
			}
		case "error":
			s.Err = strings.TrimSpace(strings.TrimPrefix(line, "error"))
		}
	}
	if s.Err == "" && s.Public.Addr == "" {
		s.Err = "the session VM printed no DNS results"
	}
	return s
}
