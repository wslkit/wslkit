# Embedded guest agent

`go run ./tools/build-agent` cross-compiles `cmd/wslkit-agent` for linux/amd64 and
linux/arm64 into this directory as `wslkit-agent-linux-<arch>`. The binaries are
git-ignored and embedded into `wslkit.exe` at build time; `wslkit agent install`
extracts the one matching the host architecture into the distro.

This file exists so the `//go:embed all:files` pattern always matches.
