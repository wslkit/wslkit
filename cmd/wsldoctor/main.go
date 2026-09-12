// wsldoctor: diagnose why WSL 2 is broken or slow, then fix it.
package main

import (
	"os"

	"github.com/wslkit/wsldoctor/internal/cli"
)

// version is set by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	app := &cli.App{Version: version, Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(app.Run(os.Args[1:]))
}
