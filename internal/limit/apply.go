package limit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds each command run inside a distribution.
const DefaultTimeout = 30 * time.Second

// Runner runs a shell script inside a distribution as root, script on stdin.
type Runner interface {
	Run(ctx context.Context, distro, script string, timeout time.Duration) (string, error)
}

// ErrNoCgroup means this WSL does not give distributions their own cgroup, so
// there is nothing to write a limit into.
type ErrNoCgroup struct{ Distro string }

func (e ErrNoCgroup) Error() string {
	return fmt.Sprintf("limit: %s has no cgroup of its own (/sys/fs/cgroup/wsl-user/distro-N). "+
		"That arrived between WSL 2.7.13 and 2.9.11; on an older runtime there is nowhere to put a "+
		"per-distribution limit, and .wslconfig caps the whole VM instead", e.Distro)
}

// readScript asks the distribution where its cgroup is and what is in it.
//
// The node comes from this shell's own cgroup rather than pid 1's: a
// distribution without systemd reports the root for pid 1 while its own
// processes sit in the distro node, so reading pid 1 finds nothing on exactly
// the distributions that are simplest to limit.
const readScript = `node=$(awk -F: '{print $3}' /proc/self/cgroup 2>/dev/null | head -1)
case "$node" in
  /wsl-user/distro-*) ;;
  *) node=$(awk -F: '{print $3}' /proc/1/cgroup 2>/dev/null | head -1) ;;
esac
node=$(echo "$node" | sed -n 's,^\(/wsl-user/distro-[0-9]*\).*,\1,p')
if [ -z "$node" ]; then
  echo "node="
  exit 0
fi
dir="/sys/fs/cgroup$node"
echo "node=$node"
echo "memory_max=$(cat "$dir/memory.max" 2>/dev/null)"
echo "memory_high=$(cat "$dir/memory.high" 2>/dev/null)"
echo "swap_max=$(cat "$dir/memory.swap.max" 2>/dev/null)"
echo "cpu_max=$(cat "$dir/cpu.max" 2>/dev/null)"
echo "memory_current=$(cat "$dir/memory.current" 2>/dev/null)"
echo "systemd=$([ -d "$dir/systemd" ] && echo yes || echo no)"
`

// Read reports what the distribution's cgroup currently says.
func Read(ctx context.Context, r Runner, distro string, timeout time.Duration) (Current, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	out, err := r.Run(ctx, distro, readScript, timeout)
	if err != nil {
		return Current{}, fmt.Errorf("limit: reading the cgroup in %s: %w: %s", distro, err, strings.TrimSpace(out))
	}
	c := Current{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "node":
			c.Node = v
		case "memory_max":
			c.MemoryMax = v
		case "memory_high":
			c.MemoryHigh = v
		case "swap_max":
			c.SwapMax = v
		case "cpu_max":
			c.CPUMax = v
		case "memory_current":
			c.MemoryCurrent, _ = strconv.ParseUint(v, 10, 64)
		case "systemd":
			c.Systemd = v == "yes"
		}
	}
	if c.Node == "" {
		return c, ErrNoCgroup{Distro: distro}
	}
	return c, nil
}

// WriteScript renders the script that applies a set of writes.
//
// Every write is checked: the kernel rejects a value it does not like by
// failing the write, and a shell redirect that silently did nothing would leave
// the tool reporting a limit that is not there.
func WriteScript(writes []Write) string {
	var b strings.Builder
	b.WriteString(readNodePrelude)
	for _, w := range writes {
		fmt.Fprintf(&b, "printf '%%s' %s > \"$dir/%s\" || { echo \"failed: %s\" >&2; exit 1; }\n",
			shellQuote(w.Value), w.File, w.File)
	}
	b.WriteString("echo applied\n")
	return b.String()
}

// readNodePrelude finds the cgroup directory again, inside the same shell that
// will write to it.
//
// Resolved at write time rather than passed in, because the node is named after
// the distribution's init pid and changes whenever the distribution restarts.
// A path read a moment ago can already be gone.
const readNodePrelude = `set -e
node=$(awk -F: '{print $3}' /proc/self/cgroup 2>/dev/null | head -1)
case "$node" in
  /wsl-user/distro-*) ;;
  *) node=$(awk -F: '{print $3}' /proc/1/cgroup 2>/dev/null | head -1) ;;
esac
node=$(echo "$node" | sed -n 's,^\(/wsl-user/distro-[0-9]*\).*,\1,p')
if [ -z "$node" ]; then
  echo "no per-distribution cgroup" >&2
  exit 2
fi
dir="/sys/fs/cgroup$node"
`

// Apply writes the limits.
func Apply(ctx context.Context, r Runner, distro string, writes []Write, timeout time.Duration) error {
	if len(writes) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	out, err := r.Run(ctx, distro, WriteScript(writes), timeout)
	if err != nil {
		return fmt.Errorf("limit: applying to %s: %w: %s", distro, err, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "applied") {
		return fmt.Errorf("limit: applying to %s: the distribution did not confirm: %s", distro, strings.TrimSpace(out))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
