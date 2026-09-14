package host

import (
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// defenderEnv is a machine with Defender on and one distribution, with the
// exclusion list as an elevated run would have read it.
func defenderEnv(ex env.Field[env.Exclusions]) *env.Env {
	e := env.New("t")
	e.UserProfile = `C:\Users\Ana`
	e.Defender.Present = env.Ok(true, "t")
	e.Defender.RealtimeEnabled = env.Ok(true, "t")
	e.Defender.Exclusions = ex

	d := env.Distro{GUID: "{a}", Name: "Ubuntu", Version: 2}
	d.Vhd = env.Ok(env.VhdInfo{Path: vhd}, "t")
	e.Distros = env.Ok([]env.Distro{d}, "t")
	return e
}

// The branch that has never run on a real machine: elevated, list readable,
// disk covered.
func TestDefenderElevatedAndExcluded(t *testing.T) {
	e := defenderEnv(env.Ok(env.Exclusions{
		Paths:     []string{`%LOCALAPPDATA%\Packages`},
		Processes: []string{"vmmem", "vmmemWSL", "wslservice.exe", "wsl.exe"},
	}, "MSFT_MpPreference"))
	r := (Defender{}).Run(e)
	if r.Status != probe.OK {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	// Which rule covered it, because a rule someone else wrote is worth
	// seeing before trusting it.
	if !strings.Contains(r.Detail, `%LOCALAPPDATA%\Packages`) {
		t.Errorf("the detail should name the rule that covered the disk: %q", r.Detail)
	}
}

func TestDefenderElevatedAndNotExcluded(t *testing.T) {
	e := defenderEnv(env.Ok(env.Exclusions{Paths: []string{`D:\something-else`}}, "MSFT_MpPreference"))
	r := (Defender{}).Run(e)
	if r.Status != probe.Warn {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	if !strings.Contains(r.Detail, "ext4.vhdx") {
		t.Errorf("the detail should name the unprotected disk: %q", r.Detail)
	}
	if r.FixID != "defender" {
		t.Errorf("fix id = %q", r.FixID)
	}
}

// The unelevated case, which is what almost every run hits: not a finding, and
// not a clean bill of health either.
func TestDefenderUnelevatedIsUnknownNotOK(t *testing.T) {
	e := defenderEnv(env.Fail[env.Exclusions](env.ErrNeedsElevation, "MSFT_MpPreference.ExclusionPath", env.ErrNeedsElevationSentinel))
	r := (Defender{}).Run(e)
	if r.Status == probe.OK || r.Status == probe.Warn {
		t.Fatalf("an unreadable list must not be reported either way: %s %q", r.Status, r.Summary)
	}
	if r.FixID != "defender" {
		t.Errorf("fix id = %q", r.FixID)
	}
}

// Real-time protection off makes the exclusion list moot, and a warning about
// it would be noise on a machine that is not scanning anything.
func TestDefenderRealtimeOff(t *testing.T) {
	e := defenderEnv(env.Ok(env.Exclusions{}, "t"))
	e.Defender.RealtimeEnabled = env.Ok(false, "t")
	if r := (Defender{}).Run(e); r.Status != probe.OK {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

// An extension rule protects the disk as surely as a path rule does, and this
// is the shape most "exclude WSL from Defender" advice takes.
func TestDefenderExtensionRuleCounts(t *testing.T) {
	e := defenderEnv(env.Ok(env.Exclusions{Extensions: []string{"vhdx"}}, "t"))
	if r := (Defender{}).Run(e); r.Status != probe.OK {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}
