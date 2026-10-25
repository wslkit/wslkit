package collect

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
)

// Some of what goes wrong inside WSL is not any one distribution's: whether
// the VM was given nested virtualization, and which handler the kernel picks
// for a Windows executable. Both are the same from every distribution, so the
// script runs once, in a distribution that is already running.
//
// The binfmt_misc table is the kernel's own. WSL's init registers WSLInterop
// in it when the VM boots (src/linux/init/main.cpp), and a distribution that
// clears or adds to it does so for every other distribution too
// (ProtectBinfmtStatus in init.cpp). Its status file is shadowed by WSL on
// systemd distributions and says nothing true, so only the entries are read.

// vmScript only reads. Each binfmt entry is printed as an entry= line followed
// by its own lines with an e: prefix, which keeps a name or an interpreter
// with spaces from being taken for another field.
const vmScript = `[ -e /dev/kvm ] && echo "kvm=yes"
grep -qwE 'vmx|svm' /proc/cpuinfo 2>/dev/null && echo "virt=yes"
echo "vendor=$(awk -F': ' '/^vendor_id/{print $2; exit}' /proc/cpuinfo 2>/dev/null)"
d=/proc/sys/fs/binfmt_misc
if [ -e "$d/register" ]; then
  echo "binfmt=mounted"
  for f in "$d"/*; do
    n=${f##*/}
    case "$n" in register|status) continue ;; esac
    [ -f "$f" ] || continue
    echo "entry=$n"
    sed 's/^/e:/' "$f" 2>/dev/null
  done
fi
`

// parseVM reads what vmScript printed.
func parseVM(out, distro string) (env.VMFacts, error) {
	v := env.VMFacts{ReadFrom: distro}
	seen := false
	var cur *env.BinfmtEntry
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if rest, ok := strings.CutPrefix(line, "e:"); ok {
			if cur != nil {
				binfmtLine(cur, strings.TrimSpace(rest))
			}
			continue
		}
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		seen = true
		switch k {
		case "kvm":
			v.KVM = val == "yes"
		case "virt":
			v.VirtFlags = val == "yes"
		case "vendor":
			v.CPUVendor = val
		case "binfmt":
			v.BinfmtMounted = val == "mounted"
		case "entry":
			v.Binfmt = append(v.Binfmt, env.BinfmtEntry{Name: val})
			cur = &v.Binfmt[len(v.Binfmt)-1]
		}
	}
	if !seen {
		return v, errors.New("the distribution printed nothing that could be read")
	}
	return v, nil
}

// binfmtLine reads one line of a binfmt_misc entry file, in the form
// fs/binfmt_misc.c prints it: "enabled", "interpreter /init", "flags: PF",
// "offset 0", "magic 4d5a", or "extension .exe".
func binfmtLine(b *env.BinfmtEntry, line string) {
	switch {
	case line == "enabled":
		b.Enabled = true
	case line == "disabled":
		b.Enabled = false
	case strings.HasPrefix(line, "interpreter "):
		b.Interpreter = strings.TrimPrefix(line, "interpreter ")
	case strings.HasPrefix(line, "flags:"):
		b.Flags = strings.TrimSpace(strings.TrimPrefix(line, "flags:"))
	case strings.HasPrefix(line, "offset "):
		b.Offset, _ = strconv.Atoi(strings.TrimPrefix(line, "offset "))
	case strings.HasPrefix(line, "magic "):
		b.Magic = strings.ToLower(strings.TrimPrefix(line, "magic "))
	case strings.HasPrefix(line, "extension "):
		b.Extension = strings.TrimPrefix(line, "extension ")
	}
}

// vmSource names what was run, for the provenance of the field.
func vmSource(distro string) string {
	return fmt.Sprintf("wsl.exe -d %s -u root --exec /bin/sh (VM facts script)", distro)
}

// pickVMDistro chooses where to read the VM facts: a running WSL 2
// distribution, the default one when it is running. ok is false when none is,
// and reason then says why.
func pickVMDistro(distros []env.Distro) (name string, ok bool, kind env.ErrKind, reason string) {
	var first string
	for _, d := range distros {
		if d.Version != 2 || !d.Running.OK() || !d.Running.Value {
			continue
		}
		if d.IsDefault {
			return d.Name, true, env.ErrNone, ""
		}
		if first == "" {
			first = d.Name
		}
	}
	if first != "" {
		return first, true, env.ErrNone, ""
	}
	for _, d := range distros {
		if d.Version == 2 && !d.Running.OK() {
			return "", false, d.Running.ErrKind, "not read: whether any distribution is running could not be determined"
		}
	}
	return "", false, env.ErrVMWakeRefused, "not read: no WSL 2 distribution is running, and starting one would start the VM"
}
