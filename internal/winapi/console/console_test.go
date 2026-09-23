//go:build windows

package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnConsoleKeepsOtherFlags(t *testing.T) {
	cmd := exec.Command("wsl.exe")
	OwnConsole(cmd)
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("no console of its own")
	}
	cmd = exec.Command("wsl.exe")
	cmd.SysProcAttr = nil
	OwnConsole(cmd)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	OwnConsole(cmd)
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Error("a flag already set was dropped")
	}
}

// wslLaunch is a file that starts wsl.exe or wslc.exe itself.
var wslLaunch = regexp.MustCompile(`exec\.Command(Context)?\([^)]*("wsl\.exe"|"wslc\.exe"|h\.exe\(\)|r\.exe\(\))`)

// The bug in #101 was fixed in one command first and was still in seven
// others, because each starts wsl.exe on its own. Every file that does must
// give it a console of its own.
func TestEveryWSLLaunchGetsItsOwnConsole(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var checked int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "site", "spikes", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if !wslLaunch.MatchString(src) {
			return nil
		}
		checked++
		launches := len(wslLaunch.FindAllString(src, -1))
		owned := strings.Count(src, "console.OwnConsole(") + strings.Count(src, "\tOwnConsole(")
		if owned < launches {
			t.Errorf("%s starts wsl.exe %d time(s) but gives it its own console %d time(s); use console.OwnConsole", path, launches, owned)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Eight files start wsl.exe or wslc.exe today; finding far fewer means
	// the pattern stopped matching and the test proves nothing.
	if checked < 8 {
		t.Errorf("only %d file(s) matched; the pattern has drifted from how wsl.exe is started", checked)
	}
}
