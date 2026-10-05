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
		"That arrived in WSL 2.9.8; on an older runtime there is nowhere to put a "+
		"per-distribution limit, and .wslconfig caps the whole VM instead", e.Distro)
}

// ErrNoNsenter means the distribution has its own cgroup namespace, so its
// cgroup can be read but only written from outside, and it has no nsenter that
// gets there. Measured on Alpine 3.24: busybox's nsenter has no -C, and
// util-linux-misc's has.
type ErrNoNsenter struct{ Distro string }

func (e ErrNoNsenter) Error() string {
	return fmt.Sprintf("limit: %s runs in a cgroup namespace of its own (WSL 2.9.13 and newer), and the kernel "+
		"refuses writes to its cgroup from inside it. wslkit writes them through WSL's own init process with "+
		"util-linux's nsenter -C, which %s does not have (busybox's nsenter has no -C). Install util-linux: "+
		"util-linux-misc on Alpine", e.Distro, e.Distro)
}

// FindNode finds the distribution's cgroup, in the shell that will use it.
//
// It sets dir to the cgroup's directory as this shell sees it, node to its path
// in the VM's tree for the report, namespaced to yes or no, and enter to the
// command that puts a write where the kernel accepts it: empty when none is
// needed, "none" when one is needed and there is no nsenter.
//
// Two layouts exist. From WSL 2.9.8 to 2.9.12 the distribution shares the VM's
// cgroup namespace and finds its node, /wsl-user/distro-N, in its own cgroup
// path. That comes from this shell rather than pid 1: a distribution without
// systemd reports the root for pid 1 while its own processes sit in the distro
// node.
//
// From 2.9.13 (microsoft/WSL#41512) each distribution has a cgroup namespace
// rooted at its node, so /proc/self/cgroup says /non-systemd and the cgroup is
// /sys/fs/cgroup itself. The real root cgroup has no memory.current, which is
// how the two are told apart. The mount is nsdelegate, and the kernel refuses
// writes to a namespace root from inside it, so writes go through the first
// ancestor of this shell outside the namespace: WSL's own init for this
// distribution. Its parent chain, not any process that differs, because a
// container inside the distribution has a namespace of its own too.
const FindNode = `node=$(awk -F: '{print $3}' /proc/self/cgroup 2>/dev/null | head -1)
case "$node" in
  /wsl-user/distro-*) ;;
  *) node=$(awk -F: '{print $3}' /proc/1/cgroup 2>/dev/null | head -1) ;;
esac
node=$(echo "$node" | sed -n 's,^\(/wsl-user/distro-[0-9]*\).*,\1,p')
dir="/sys/fs/cgroup$node"
enter=""
namespaced=no
if [ -z "$node" ] && [ -r /sys/fs/cgroup/memory.current ]; then
  dir=/sys/fs/cgroup
  node=/
  namespaced=yes
  self=$(readlink /proc/self/ns/cgroup)
  outside=""
  p=$$
  while [ -n "$p" ] && [ "$p" -ge 1 ]; do
    if [ "$(readlink "/proc/$p/ns/cgroup" 2>/dev/null)" != "$self" ]; then
      outside=$p
      break
    fi
    [ "$p" -eq 1 ] && break
    p=$(awk '/^PPid:/{print $2}' "/proc/$p/status" 2>/dev/null)
  done
  # Tried rather than looked for: busybox has an nsenter without -C.
  if [ -n "$outside" ] && nsenter -t "$outside" -C -- true >/dev/null 2>&1; then
    enter="nsenter -t $outside -C --"
    n=$($enter awk -F: '{print $3}' /proc/self/cgroup 2>/dev/null | head -1 | sed -n 's,^\(/wsl-user/distro-[0-9]*\).*,\1,p')
    [ -n "$n" ] && node=$n
  else
    enter=none
  fi
fi
`

// readScript asks the distribution where its cgroup is and what is in it.
const readScript = FindNode + `if [ -z "$node" ]; then
  echo "node="
  exit 0
fi
echo "node=$node"
echo "namespaced=$namespaced"
echo "writable=$([ "$enter" = none ] && echo no || echo yes)"
echo "memory_max=$(cat "$dir/memory.max" 2>/dev/null)"
echo "memory_high=$(cat "$dir/memory.high" 2>/dev/null)"
echo "swap_max=$(cat "$dir/memory.swap.max" 2>/dev/null)"
echo "cpu_max=$(cat "$dir/cpu.max" 2>/dev/null)"
echo "memory_current=$(cat "$dir/memory.current" 2>/dev/null)"
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
		case "namespaced":
			c.Namespaced = v == "yes"
		case "writable":
			c.Writable = v == "yes"
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
//
// The node is found again inside the writing shell rather than passed in,
// because it is named after the distribution's init pid and changes whenever
// the distribution restarts. A path read a moment ago can already be gone.
func WriteScript(writes []Write) string {
	var b strings.Builder
	b.WriteString(FindNode)
	b.WriteString(writePrelude)
	for _, w := range writes {
		fmt.Fprintf(&b, "w %s %s || { echo \"failed: %s\" >&2; exit 1; }\n", w.File, shellQuote(w.Value), w.File)
	}
	b.WriteString("echo applied\n")
	return b.String()
}

// writePrelude stops where there is nothing to write into, and defines w, which
// writes one file from wherever the kernel accepts it.
const writePrelude = `if [ -z "$node" ]; then
  echo "no per-distribution cgroup" >&2
  exit 2
fi
if [ "$enter" = none ]; then
  echo "no-nsenter" >&2
  exit 3
fi
w() { $enter sh -c 'printf "%s" "$1" > "$2"' w "$2" "$dir/$1"; }
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
		if strings.Contains(out, "no-nsenter") {
			return ErrNoNsenter{Distro: distro}
		}
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
