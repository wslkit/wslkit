package actions

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/fix"
)

func defenderEnv() *env.Env {
	e := env.New("t")
	e.Defender.Present = env.Ok(true, "t")
	e.Defender.RealtimeEnabled = env.Ok(true, "t")
	e.Distros = env.Ok([]env.Distro{
		{Name: "Ubuntu", Version: 2, Vhd: env.Ok(env.VhdInfo{Path: `C:\Users\u\AppData\Local\wsl\{g1}\ext4.vhdx`}, "t")},
		{Name: "Old", Version: 1},
		{Name: "Alpine", Version: 2, Vhd: env.Ok(env.VhdInfo{Path: `D:\wsl\alpine\ext4.vhdx`}, "t")},
	}, "t")
	return e
}

func TestDefenderPlanAddsMissingOnly(t *testing.T) {
	e := defenderEnv()
	e.Defender.Exclusions = env.Ok(env.Exclusions{
		Paths:     []string{`D:\wsl\alpine\ext4.vhdx`},
		Processes: []string{"VMMEM"},
	}, "t")
	p, err := Defender{}.Plan(e, fix.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Elevates || len(p.Steps) != 2 || p.Steps[0].Kind != "wmi_method" || len(p.Rollback) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	call, err := fix.DecodeWMIMethod(p.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if call.Class != "MSFT_MpPreference" || call.Method != "Add" || call.Namespace != `root\Microsoft\Windows\Defender` {
		t.Fatalf("call = %+v", call)
	}
	wantPaths := []string{`C:\Users\u\AppData\Local\wsl\{g1}\ext4.vhdx`}
	if !reflect.DeepEqual(call.Params["ExclusionPath"], wantPaths) {
		t.Fatalf("paths = %v", call.Params["ExclusionPath"])
	}
	if !reflect.DeepEqual(call.Params["ExclusionProcess"], []string{"vmmemWSL", "wslservice.exe"}) {
		t.Fatalf("procs = %v", call.Params["ExclusionProcess"])
	}
	rb, _ := fix.DecodeWMIMethod(p.Rollback[0])
	if rb.Method != "Remove" || !reflect.DeepEqual(rb.Params, call.Params) {
		t.Fatalf("rollback must mirror add: %+v", rb)
	}
	rec := &fix.Recording{}
	if err := fix.Apply(p, rec); err != nil || len(rec.Steps) != 2 {
		t.Fatal(err)
	}
	if !strings.Contains(fix.Describe(p), "MSFT_MpPreference.Add") {
		t.Fatal("describe should show the WMI call")
	}
}

func TestDefenderPlanEdgeCases(t *testing.T) {
	e := defenderEnv()
	e.Defender.Exclusions = env.Ok(env.Exclusions{
		Paths:     []string{`C:\Users\u\AppData\Local\wsl\{g1}\ext4.vhdx\`, `d:\wsl\alpine\ext4.vhdx`},
		Processes: []string{"vmmem", "vmmemWSL", "wslservice.exe"},
	}, "t")
	p, err := Defender{}.Plan(e, fix.Options{})
	if err != nil || len(p.Steps) != 1 || p.Steps[0].Kind != "note" {
		t.Fatalf("all present -> %+v %v", p.Steps, err)
	}

	e = defenderEnv()
	e.Defender.Exclusions = env.Fail[env.Exclusions](env.ErrNeedsElevation, "t", nil)
	p, err = Defender{}.Plan(e, fix.Options{})
	if err != nil || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "could not be read") {
		t.Fatalf("unreadable exclusions -> %v %v", p.Warnings, err)
	}

	e = defenderEnv()
	e.Defender.Present = env.Ok(false, "t")
	if _, err := (Defender{}).Plan(e, fix.Options{}); err == nil {
		t.Fatal("no Defender must be an error")
	}
	e = defenderEnv()
	e.Distros = env.Ok([]env.Distro{}, "t")
	if _, err := (Defender{}).Plan(e, fix.Options{}); err == nil {
		t.Fatal("no disks must be an error")
	}
}

func TestWMIMethodRoundTrip(t *testing.T) {
	s := fix.WMIMethod("d", "ns", "C", "M", map[string]interface{}{"A": []string{"x", "y"}, "B": true, "N": 3})
	c, err := fix.DecodeWMIMethod(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Params["A"], []string{"x", "y"}) || c.Params["B"] != true || c.Params["N"] != float64(3) {
		t.Fatalf("params = %#v", c.Params)
	}
	if _, err := fix.DecodeWMIMethod(fix.Step{Kind: "exec"}); err == nil {
		t.Fatal("wrong kind must fail")
	}
}

// A disk already covered by a folder rule, a wildcard or the extension list
// does not need a rule of its own. Adding one anyway leaves the user an
// exclusion to wonder about later, and makes the fix look like it did
// something when there was nothing to do.
func TestDefenderPlanSkipsWhatIsAlreadyCovered(t *testing.T) {
	cases := map[string]env.Exclusions{
		"the folder above":    {Paths: []string{`C:\Users\u\AppData\Local\wsl`, `D:\wsl`}},
		"a wildcard per user": {Paths: []string{`C:\Users\*\AppData\Local\wsl`, `D:\wsl\*`}},
		"the vhdx extension":  {Extensions: []string{"vhdx"}},
		"an environment name": {Paths: []string{`%LOCALAPPDATA%\wsl`, `D:\wsl`}},
	}
	for name, ex := range cases {
		t.Run(name, func(t *testing.T) {
			e := defenderEnv()
			e.UserProfile = `C:\Users\u`
			ex.Processes = []string{"vmmem", "vmmemWSL", "wslservice.exe"}
			e.Defender.Exclusions = env.Ok(ex, "t")
			p, err := Defender{}.Plan(e, fix.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Steps) != 1 || p.Steps[0].Kind != "note" {
				t.Fatalf("plan should have nothing to do: %+v", p.Steps)
			}
		})
	}
}
