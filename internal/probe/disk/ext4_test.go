package disk

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/ext4"
	"github.com/wslkit/wslkit/internal/probe"
)

func fsEnv(distros ...env.Distro) *env.Env {
	e := env.New("t")
	e.Distros = env.Ok(distros, "t")
	return e
}

func clean() ext4.Super {
	return ext4.Super{State: ext4.StateCleanlyUnmounted, Clean: true, BlockSize: 4096, Blocks: 1 << 28, LastCheck: 1789000000}
}

func errored() ext4.Super {
	s := ext4.Super{State: ext4.StateErrors, HasErrors: true, ErrorCount: 3, BlockSize: 4096}
	s.First = ext4.ErrorEvent{Time: 1788264000, Func: "ext4_lookup", Line: 1234, Inode: 12}
	s.Last = ext4.ErrorEvent{Time: 1789290000, Func: "ext4_iget", Line: 4321}
	return s
}

func TestFilesystemClean(t *testing.T) {
	e := fsEnv(env.Distro{Name: "u", Version: 2, Ext4: env.Ok(clean(), "t")})
	r := (Filesystem{}).Run(e)
	if r.Status != probe.OK {
		t.Fatalf("clean -> %s (%s)", r.Status, r.Summary)
	}
	if !strings.Contains(r.Detail, "u: no errors recorded") {
		t.Errorf("detail = %q", r.Detail)
	}
}

func TestFilesystemErrors(t *testing.T) {
	e := fsEnv(
		env.Distro{Name: "broken", Version: 2, Ext4: env.Ok(errored(), "t")},
		env.Distro{Name: "fine", Version: 2, Ext4: env.Ok(clean(), "t")},
	)
	r := (Filesystem{}).Run(e)
	if r.Status != probe.Fail {
		t.Fatalf("errors -> %s", r.Status)
	}
	if !strings.Contains(r.Summary, "broken") || !strings.Contains(r.Summary, "3 errors") {
		t.Errorf("summary = %q", r.Summary)
	}
	for _, want := range []string{"ext4_lookup:1234", "ext4_iget:4321", "fine: no errors recorded"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail missing %q:\n%s", want, r.Detail)
		}
	}
	if !strings.Contains(r.FixHint, "wsl --system -u root -- e2fsck -n") || !strings.Contains(r.FixHint, "wsl --export") {
		t.Errorf("fix hint should back up before it checks: %q", r.FixHint)
	}
}

// The errors bit outlives the boot that set it even when the count was reset,
// so the bit alone is a finding.
func TestFilesystemErrorBitWithoutCount(t *testing.T) {
	s := clean()
	s.Clean = false
	s.HasErrors = true
	s.State = ext4.StateErrors
	r := (Filesystem{}).Run(fsEnv(env.Distro{Name: "u", Version: 2, Ext4: env.Ok(s, "t")}))
	if r.Status != probe.Fail {
		t.Fatalf("errors bit -> %s", r.Status)
	}
	if !strings.Contains(r.Detail, "not cleanly unmounted") {
		t.Errorf("detail = %q", r.Detail)
	}
}

// A running distribution's file is a snapshot of what the guest last wrote, and
// saying so is the difference between evidence and a guess.
func TestFilesystemSaysWhenTheDistroIsRunning(t *testing.T) {
	e := fsEnv(env.Distro{Name: "u", Version: 2, Ext4: env.Ok(errored(), "t"), Running: env.Ok(true, "t")})
	if r := (Filesystem{}).Run(e); !strings.Contains(r.Detail, "may be newer") {
		t.Errorf("detail = %q", r.Detail)
	}
}

func TestFilesystemUncollectedAndUnreadable(t *testing.T) {
	t.Run("nothing collected", func(t *testing.T) {
		r := (Filesystem{}).Run(fsEnv(env.Distro{Name: "u", Version: 2}))
		if r.Status != probe.Skipped {
			t.Fatalf("uncollected -> %s", r.Status)
		}
	})
	t.Run("wsl 1 has no vhdx", func(t *testing.T) {
		r := (Filesystem{}).Run(fsEnv(env.Distro{Name: "old", Version: 1}))
		if r.Status != probe.Skipped {
			t.Fatalf("wsl 1 -> %s", r.Status)
		}
	})
	t.Run("not ext4", func(t *testing.T) {
		e := fsEnv(env.Distro{Name: "u", Version: 2, Ext4: env.Absent[ext4.Super]("t")})
		r := (Filesystem{}).Run(e)
		if r.Status != probe.Skipped {
			t.Fatalf("not ext4 -> %s, want a probe that keeps quiet", r.Status)
		}
	})
	t.Run("needs elevation", func(t *testing.T) {
		e := fsEnv(env.Distro{Name: "u", Version: 2, Ext4: env.Fail[ext4.Super](env.ErrNeedsElevation, "t", nil)})
		r := (Filesystem{}).Run(e)
		if r.Status != probe.Unknown || !r.Elevate {
			t.Fatalf("elevation -> %s elevate=%v", r.Status, r.Elevate)
		}
	})
	t.Run("read error", func(t *testing.T) {
		e := fsEnv(env.Distro{Name: "u", Version: 2, Ext4: env.Fail[ext4.Super](env.ErrOther, "t", errRead{})})
		r := (Filesystem{}).Run(e)
		if r.Status != probe.Unknown || r.Elevate {
			t.Fatalf("read error -> %s elevate=%v", r.Status, r.Elevate)
		}
		if !strings.Contains(r.Detail, "block 3 is only partially present") {
			t.Errorf("detail = %q", r.Detail)
		}
	})
}

type errRead struct{}

func (errRead) Error() string { return "vhdx: block 3 is only partially present (differencing disk)" }
