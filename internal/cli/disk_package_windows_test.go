//go:build windows

package cli

import (
	"os"
	"strings"
	"testing"
)

// #91: the guard is only as good as the commands that call it. The CLI reads
// the real registry, so this checks the wiring in the source instead: every
// command that rehomes or unregisters a disk takes --force and refuses a
// package-owned distribution without it. A new command of that kind belongs
// in this list.
func TestDiskCommandsGuardPackagedDistributions(t *testing.T) {
	for file, what := range map[string]string{
		"disk_move_windows.go":    "move",
		"disk_relink_windows.go":  "relink",
		"disk_trash_windows.go":   "trash",
		"disk_rebuild_windows.go": "rebuild",
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, `disk.GuardPackaged(r, "`+what+`", f.force)`) {
			t.Errorf("%s does not guard a package-owned distribution", file)
		}
		if !strings.Contains(src, "f.registerForce(fs)") {
			t.Errorf("%s does not take --force", file)
		}
	}
}
