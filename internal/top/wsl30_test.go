package top

import (
	"os"
	"strings"
	"testing"
)

// Captured with `wslkit top --raw` on WSL 3.0.1, the first stable release with
// a cgroup namespace per distribution (microsoft/WSL#41512, from 2.9.13).
// Inside one, /proc/self/cgroup says /non-systemd and /sys/fs/cgroup is the
// distribution's own cgroup. Ubuntu runs systemd; skrog-engine does not;
// Alpine 3.24 has only busybox, whose nsenter has no -C, so its node in the
// VM's tree cannot be named and reads as /.
func wsl30(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/wsl-3.0.1-" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWSL30TakesTheCgroupMethod(t *testing.T) {
	for name, path := range map[string]string{
		"systemd":    "/wsl-user/distro-22439",
		"no-systemd": "/wsl-user/distro-141",
		"busybox":    "/",
	} {
		s, _, err := ParseSample(name, wsl30(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Method != MethodCgroup || s.CgroupPath != path {
			t.Errorf("%s: method %q, path %q", name, s.Method, s.CgroupPath)
		}
		if s.MemoryBytes == 0 || s.AnonBytes == nil || s.PIDs == nil || s.Pressure == nil || s.ReadBytes == nil {
			t.Errorf("%s: a cgroup counter is missing: %+v", name, s)
		}
	}
}

// Inside the namespace, /sys/fs/cgroup/*/ are the distribution's own children
// (Ubuntu's system.slice and init.scope), already counted in its row. Reported
// as VM-root groups they would count its memory twice, the same leak the 2.7
// guard exists for.
func TestWSL30ReportsNoGroups(t *testing.T) {
	for _, name := range []string{"systemd", "no-systemd", "busybox"} {
		out := wsl30(t, name)
		if strings.Contains(out, "\ng.") {
			t.Errorf("%s: the script printed group lines inside a cgroup namespace", name)
		}
		if g := ParseGroups(out); len(g) != 0 {
			t.Errorf("%s: groups %+v", name, g)
		}
	}
}

// The fixtures prove the parsing; these prove the script that produced them.
// The script that shipped before WSL 3.0 looked only for /wsl-user/distro-N in
// /proc/self/cgroup, found /non-systemd, and fell back to summing /proc.
func TestDistroScriptFindsANamespacedCgroup(t *testing.T) {
	if !strings.Contains(SampleScript, "[ -r /sys/fs/cgroup/memory.current ]") {
		t.Error("the script must recognise a namespace root by its memory.current")
	}
	if !strings.Contains(SampleScript, `[ "$namespaced" = no ]`) {
		t.Error("the group rows must be gated on not being in a namespace")
	}
}
