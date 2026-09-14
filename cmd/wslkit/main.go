// wslkit: a toolkit for WSL 2. Troubleshooting and maintenance commands —
// doctor, disk, top, limit, proxy, guard, agent, sock — in one binary.
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
	app := &cli.App{Version: version, Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}
	os.Exit(app.Run(args))
}

// invokedAs returns the lower-case base name of the executable without extension.
func invokedAs(argv0 string) string {
	base := strings.ToLower(filepath.Base(argv0))
	return strings.TrimSuffix(base, ".exe")
}
