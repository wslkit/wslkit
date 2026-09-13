# wslkit

> One binary of tools for WSL 2. Diagnose why it is broken or slow, reclaim the
> space its disks are holding, and bridge Windows into a distribution.

[![ci](https://github.com/wslkit/wslkit/actions/workflows/ci.yml/badge.svg)](https://github.com/wslkit/wslkit/actions/workflows/ci.yml)
[![docs](https://github.com/wslkit/wslkit/actions/workflows/docs.yml/badge.svg)](https://wslkit.github.io/wslkit/)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![WSL 2](https://img.shields.io/badge/WSL-2-blue.svg)](https://learn.microsoft.com/windows/wsl/)

**[Documentation](https://wslkit.github.io/wslkit/)**

```
wslkit doctor                    read-only, no administrator, ranked diagnosis
wslkit doctor explain "Wsl/Service/E_UNEXPECTED"
wslkit disk list                 what each distribution costs on disk
wslkit disk compact Ubuntu       trim, stop, then shrink the file
wslkit top                       what the utility VM is using, and which distribution
```

No installer, no PowerShell, no administrator for anything that only reads.
Nothing changes unless you name a command that changes it, and every change
prints its plan first and records a rollback.

## Install

No releases yet, so build it. Needs Go 1.27 and nothing else: no cgo, no C
compiler.

```
go run ./tools/build-agent -version dev
go build -o wslkit.exe ./cmd/wslkit
```

Windows 10 build 19041 or later, amd64 or arm64.
[Full instructions](https://wslkit.github.io/wslkit/install/).

## What it does

| | |
|---|---|
| [`doctor`](https://wslkit.github.io/wslkit/doctor/) | why WSL is broken or slow, and fixes with journalled undo |
| [`disk`](https://wslkit.github.io/wslkit/disk/) | inspect, compact, move and repair distribution disks |
| [`top`](https://wslkit.github.io/wslkit/top/) | what the utility VM is using, and which distribution |
| [`agent`](https://wslkit.github.io/wslkit/agent/) | a helper inside a distribution, and the Windows daemon it talks to |
| [`sock`](https://wslkit.github.io/wslkit/sock/) | your Windows SSH and GPG keys, usable from inside WSL |

Guides for the things people actually arrive with:
[WSL will not start](https://wslkit.github.io/wslkit/wsl-will-not-start/),
[reclaim disk space](https://wslkit.github.io/wslkit/reclaim-disk-space/),
[use your Windows keys](https://wslkit.github.io/wslkit/windows-keys-in-wsl/),
[report a bug well](https://wslkit.github.io/wslkit/report-a-bug/).

## Why it exists

WSL fails with an error code and no explanation, and the answer is usually a
handful of Windows facts nobody surfaces together: which optional features are
on, which WSL you have, whether something else took the hypervisor, whether your
distribution needs a runtime newer than the one installed.

`wslkit doctor` collects those facts once, with provenance, and reasons about
them. A check that could not read something reports that it could not, rather
than guessing.

The design, and what it rules out, is in the
[decision records](https://wslkit.github.io/wslkit/decisions/).

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md), and the
[contributing page](https://wslkit.github.io/wslkit/contributing/) for the short
version. `docs/` is the source of truth for the site; the reference pages are
generated from the data the binary embeds, so they cannot drift.

Commits, pull requests and issues carry no AI attribution.

## Part of wslkit

- [wsldrive](https://github.com/wslkit/wsldrive) — cross the WSL 2 filesystem boundary at native speed
- [skrog](https://github.com/wslkit/skrog) — the upstream Docker Engine on Windows via WSL 2
- [wsldisk](https://github.com/wslkit/wsldisk) — archived; it is now `wslkit disk`

## Licence

[MIT](LICENSE).
