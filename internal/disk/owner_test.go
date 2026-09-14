package disk

import (
	"strings"
	"testing"
)

// The whole point of naming the owner is the disk somebody is about to delete
// because nothing claimed it. Docker Desktop's is the one that costs the most.
func TestIdentifyOwner(t *testing.T) {
	cases := map[string]string{
		`C:\Users\ana\AppData\Local\Docker\wsl\disk\docker_data.vhdx`:                         "Docker Desktop",
		`C:\Users\ana\AppData\Local\Docker\wsl\data\ext4.vhdx`:                                "Docker Desktop",
		`C:\ProgramData\rancher-desktop\distro-data\ext4.vhdx`:                                "Rancher Desktop",
		`C:\Users\ana\.local\share\containers\podman\machine\wsl\podman-machine-default.vhdx`: "Podman",
		`C:\Users\ana\AppData\Local\wslkit\trash\Ubuntu-2026-09-13\ext4.vhdx`:                 "wslkit",
		`D:\disks\something.vhdx`:                                                             "",
	}
	for path, want := range cases {
		if got := IdentifyOwner(path).Name; got != want {
			t.Errorf("IdentifyOwner(%q).Name = %q, want %q", path, got, want)
		}
	}
}

// A disk under Packages is a Store distribution's, which may simply have been
// unregistered. There is no application name to give, but there is something
// worth saying about what it holds.
func TestPackagesDiskHasNoNameButAWarning(t *testing.T) {
	o := IdentifyOwner(`C:\Users\ana\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState\ext4.vhdx`)
	if o.Name != "" {
		t.Errorf("name = %q, want none", o.Name)
	}
	if !strings.Contains(o.Holds, "unregistered") {
		t.Errorf("holds = %q", o.Holds)
	}
	if o.Describe() != "unknown" {
		t.Errorf("describe = %q", o.Describe())
	}
}

// Matching is case-insensitive, because a path is whatever the user typed.
func TestIdentifyOwnerIgnoresCase(t *testing.T) {
	if IdentifyOwner(`C:\USERS\ANA\APPDATA\LOCAL\DOCKER\WSL\DATA\EXT4.VHDX`).Name != "Docker Desktop" {
		t.Error("an upper-cased path is the same path")
	}
}
