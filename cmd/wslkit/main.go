// wslkit: tools for WSL 2. `wslkit doctor` diagnoses why WSL is broken or slow
// and fixes it.
//
// The binary is multi-call: invoked through a copy or shim named wsldoctor.exe
// it behaves as `wslkit doctor`, so older shortcuts keep working.
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wslkit/wslkit/internal/cli"
)

// version is set by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

// aliases maps legacy binary names to the subcommand they stand for.
var aliases = map[string]string{
	"wsldoctor": "doctor",
}

func main() {
	args := os.Args[1:]
	if sub, ok := aliases[invokedAs(os.Args[0])]; ok {
		args = append([]string{sub}, args...)
	}
	app := &cli.App{Version: version, Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(app.Run(args))
}

// invokedAs returns the lower-case base name of the executable without extension.
func invokedAs(argv0 string) string {
	base := strings.ToLower(filepath.Base(argv0))
	return strings.TrimSuffix(base, ".exe")
}
