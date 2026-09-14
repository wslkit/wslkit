package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"
)

// DefaultTimeout bounds each command run inside a distribution. Writing five
// small files is instant; a minute means the distribution is not answering.
const DefaultTimeout = time.Minute

// Runner runs a shell script inside a distribution as root, with the script on
// standard input. It is an interface so every decision below can be tested
// against a fake distribution rather than a real one.
type Runner interface {
	Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error)
}

// Change is one file that applying would write, or that reverting would strip.
type Change struct {
	File File
	// Before and After are the file's whole content either side, so a dry
	// run can show exactly what changes rather than describing it.
	Before, After string
	// Existed says whether the file was there beforehand.
	Existed bool
	// Removes says this change deletes the file rather than rewriting it.
	Removes bool
}

// Changed reports whether this would actually alter anything. Re-applying the
// same settings changes nothing, and saying so is better than reporting work
// that did not happen.
func (c Change) Changed() bool {
	if c.Removes {
		return c.Existed
	}
	return c.Before != c.After
}

// PlanApply works out what applying these settings would do, by reading what is
// there now. It writes nothing, which is what makes the dry run trustworthy.
func PlanApply(ctx context.Context, r Runner, distro string, s Settings, timeout time.Duration) ([]Change, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	files := Files(s)
	if len(files) == 0 {
		return nil, fmt.Errorf("proxy: there is no proxy to apply")
	}
	var out []Change
	for _, f := range files {
		before, existed, err := readFile(ctx, r, distro, f.Path, timeout)
		if err != nil {
			return nil, err
		}
		after := f.Content
		if !f.Whole {
			after = Merge(before, f.Content)
		}
		out = append(out, Change{File: f, Before: before, After: after, Existed: existed})
	}
	return out, nil
}

// PlanRevert works out what removing wslkit's proxy configuration would do.
//
// It is driven by the fixed list of paths rather than by what the settings are
// now, so a revert still finds everything after the proxy itself has changed.
func PlanRevert(ctx context.Context, r Runner, distro string, timeout time.Duration) ([]Change, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	var out []Change
	// Any non-empty settings produce the same list of paths; only the
	// contents differ, and a revert does not need them.
	for _, f := range Files(Settings{HTTP: "http://placeholder:1", HTTPS: "http://placeholder:1"}) {
		before, existed, err := readFile(ctx, r, distro, f.Path, timeout)
		if err != nil {
			return nil, err
		}
		if !existed {
			continue
		}
		if f.Whole {
			out = append(out, Change{File: f, Before: before, After: "", Existed: true, Removes: true})
			continue
		}
		after, found := Unmerge(before)
		if !found {
			// Somebody else's /etc/environment, with no block of ours in
			// it. Leaving it alone is the whole point of the markers.
			continue
		}
		out = append(out, Change{File: f, Before: before, After: after, Existed: true})
	}
	return out, nil
}

// Apply carries out the changes.
//
// Each file is written through a temporary file and renamed, so a distribution
// that dies mid-write is left with the old file rather than half of the new one.
func Apply(ctx context.Context, r Runner, distro string, changes []Change, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	for _, c := range changes {
		if !c.Changed() {
			continue
		}
		if c.Removes {
			script := "rm -f " + shellQuote(c.File.Path) + "\n"
			if c.File.OwnsDir {
				// Only when it is empty, so a user who put something of
				// their own in there keeps it. An empty /etc/wslkit left
				// behind is the kind of litter that makes somebody wonder
				// what else was not cleaned up.
				script += "rmdir " + shellQuote(dirOf(c.File.Path)) + " 2>/dev/null || true\n"
			}
			if out, err := r.Run(ctx, distro, script, timeout); err != nil {
				return fmt.Errorf("proxy: removing %s from %s: %w: %s", c.File.Path, distro, err, strings.TrimSpace(out))
			}
			continue
		}
		if err := writeFile(ctx, r, distro, c.File, c.After, timeout); err != nil {
			return err
		}
	}
	return nil
}

// absentMarker is what the read script prints for a file that is not there. It
// is deliberately not something a configuration file would contain.
const absentMarker = "__wslkit_absent__"

// readFile reads one file from inside the distribution.
//
// The content comes back base64-encoded, because a file with CRLF line endings,
// a UTF-8 comment or a trailing newline that matters has to survive the round
// trip exactly for the comparison to mean anything.
func readFile(ctx context.Context, r Runner, distro, path string, timeout time.Duration) (content string, existed bool, err error) {
	q := shellQuote(path)
	script := fmt.Sprintf("if [ -f %s ]; then base64 < %s; else echo %s; fi\n", q, q, absentMarker)
	out, err := r.Run(ctx, distro, script, timeout)
	if err != nil {
		return "", false, fmt.Errorf("proxy: reading %s in %s: %w: %s", path, distro, err, strings.TrimSpace(out))
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == absentMarker {
		return "", false, nil
	}
	b, derr := base64.StdEncoding.DecodeString(stripSpace(trimmed))
	if derr != nil {
		return "", false, fmt.Errorf("proxy: reading %s in %s: the guest answered with something that is not base64: %q", path, distro, trimmed)
	}
	return string(b), true, nil
}

// heredocTag ends the block carrying a file's content. The content is base64,
// so it cannot contain this tag, a quote, or anything else a shell would read.
const heredocTag = "WSLKIT_CONTENT_EOF"

// WriteScript is the script that writes one file. It is built here, rather than
// in the runner, so a test can read exactly what would run inside somebody's
// distribution.
//
// The file is written to a temporary name and renamed over the target, so a
// distribution that dies mid-write is left with the old file rather than half
// of the new one. The content rides in a quoted heredoc, which is what makes
// the script safe: nothing in a proxy URL, a bypass list or a systemd directive
// is parsed by the shell, because by the time it gets there it is base64.
func WriteScript(f File, content string) string {
	mode := f.Mode
	if mode == "" {
		mode = "0644"
	}
	q := shellQuote(f.Path)
	tmp := shellQuote(f.Path + ".wslkit-tmp")
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nmkdir -p %s\nbase64 -d > %s <<'%s'\n", shellQuote(dirOf(f.Path)), tmp, heredocTag)
	// Wrapped, because a single line of base64 for a long file is awkward to
	// read in a dry run and some shells have a line-length limit.
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\n")
	fmt.Fprintf(&b, "%s\nchmod %s %s\nmv -f %s %s\n", heredocTag, mode, tmp, tmp, q)
	return b.String()
}

// writeFile writes one file inside the distribution.
func writeFile(ctx context.Context, r Runner, distro string, f File, content string, timeout time.Duration) error {
	out, err := r.Run(ctx, distro, WriteScript(f, content), timeout)
	if err != nil {
		return fmt.Errorf("proxy: writing %s in %s: %w: %s", f.Path, distro, err, strings.TrimSpace(out))
	}
	return nil
}

// dirOf is the parent of a Linux path.
func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

// shellQuote wraps a value in single quotes for /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// stripSpace removes the line breaks base64 output carries.
func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
}

// CheckReachable asks a distribution whether it can open a TCP connection to an
// address on the host.
//
// This is the check that matters for `proxy serve`, and it can only be made
// from inside: measured on Windows 10, the host firewall blocks inbound
// connections from the WSL subnet by default, so a proxy that works perfectly
// when tested from Windows is unreachable from the distribution it is for.
// Testing from the right side of the boundary is the difference between a
// working setup and an afternoon.
func CheckReachable(ctx context.Context, r Runner, distro, hostPort string, timeout time.Duration) (bool, string, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false, "", fmt.Errorf("proxy: %q is not an address: %w", hostPort, err)
	}
	// /dev/tcp is a bash feature and not every distribution has bash, so the
	// script tries the tools in the order they are likely to exist. Each one
	// answers the same question: can a TCP connection be opened at all.
	script := fmt.Sprintf(`
if command -v nc >/dev/null 2>&1; then
  nc -z -w 3 %s %s >/dev/null 2>&1 && echo reachable || echo blocked
elif command -v curl >/dev/null 2>&1; then
  curl -s -m 3 -o /dev/null telnet://%s:%s && echo reachable || echo blocked
elif [ -n "$BASH_VERSION" ] || command -v bash >/dev/null 2>&1; then
  bash -c 'exec 3<>/dev/tcp/%s/%s' >/dev/null 2>&1 && echo reachable || echo blocked
else
  echo unknown
fi
`, shellQuote(host), shellQuote(port), host, port, host, port)

	out, err := r.Run(ctx, distro, script, timeout)
	if err != nil {
		return false, "", fmt.Errorf("proxy: testing the connection from %s: %w: %s", distro, err, strings.TrimSpace(out))
	}
	switch answer := strings.TrimSpace(out); answer {
	case "reachable":
		return true, "the distribution can open a connection to " + hostPort, nil
	case "blocked":
		return false, "the distribution cannot open a connection to " + hostPort, nil
	default:
		return false, "the distribution has none of nc, curl or bash, so this could not be tested", nil
	}
}

// FirewallRule is the command that lets a distribution reach a port on the
// host.
//
// Measured on Windows 10 22H2: without it, a listener on the WSL gateway
// answers Windows and refuses the distribution, which is the confusing way
// round. The rule is scoped to the WSL subnet rather than opening the port to
// the network the laptop is on.
func FirewallRule(port int, subnet string) string {
	if subnet == "" {
		subnet = "172.16.0.0/12"
	}
	return fmt.Sprintf(`New-NetFirewallRule -DisplayName "wslkit proxy" -Direction Inbound `+
		`-Action Allow -Protocol TCP -LocalPort %d -RemoteAddress %s`, port, subnet)
}
