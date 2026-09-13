// build-agent cross-compiles the guest agent (cmd/wslkit-agent) for linux/amd64
// and linux/arm64 into internal/agent/payload/files so it can be embedded into
// wslkit.exe. Run from the repository root:
//
//	go run ./tools/build-agent [-version v]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	version := flag.String("version", "dev", "value for main.version in the agent")
	out := flag.String("out", filepath.Join("internal", "agent", "payload", "files"), "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		dst := filepath.Join(*out, "wslkit-agent-linux-"+arch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+*version, "-o", dst, "./cmd/wslkit-agent")
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fail(fmt.Errorf("build linux/%s: %w", arch, err))
		}
		fi, _ := os.Stat(dst)
		fmt.Printf("built %s (%d bytes)\n", dst, fi.Size())
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
