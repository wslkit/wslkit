package redact

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	r := Rules{UserProfile: `C:\Users\jane`, Hostname: "JANE-PC"}
	in := `path C:\Users\jane\AppData\Local\wsl\x, json "C:\\Users\\jane\\.wslconfig", other C:\Users\bob\x, sid S-1-5-21-1826575991-3325180199-2433123093-1001, host JANE-PC done`
	out := String(in, r)
	for _, bad := range []string{`\jane`, `\\jane`, `\bob`, "3325180199", "JANE-PC"} {
		if strings.Contains(out, bad) {
			t.Errorf("output still contains %q: %s", bad, out)
		}
	}
	for _, good := range []string{`%USERPROFILE%\AppData`, `%USERPROFILE%\\.wslconfig`, `C:\Users\<user>\x`, `S-1-5-21-<redacted>`, "<host>"} {
		if !strings.Contains(out, good) {
			t.Errorf("output missing %q: %s", good, out)
		}
	}
}
