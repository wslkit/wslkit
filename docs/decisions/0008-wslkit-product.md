# ADR 0008 — wslkit is the product; doctor is a subcommand

Status: accepted, 2026-09-12.

## Decision

The project is renamed from wsldoctor to **wslkit**: one binary, one package, one
signing identity, one release for every WSL tool in the wslkit organisation.

- Repository `wslkit/wslkit`, module `github.com/wslkit/wslkit`, binary `wslkit.exe`.
- Everything implemented so far lives under `wslkit doctor`: `doctor [check]`,
  `doctor explain`, `doctor fix`, `doctor undo`. Bare `wslkit doctor` runs `check`, as
  `brew doctor` and `flutter doctor` do.
- Further tools become sibling subcommands (`wslkit disk`, `wslkit log`, `wslkit limit`,
  `wslkit proxy`, ...). They reuse the platform layer built for the doctor:
  `internal/env` collectors, `internal/winapi`, `internal/wslconfig`, `internal/data`,
  `internal/render`, `internal/redact`, and the `internal/fix` plan / executor / journal.
- The binary is multi-call: invoked through a copy or shim named `wsldoctor.exe` it
  behaves as `wslkit doctor`. Package managers add a `wsldoctor` shim so the old name
  keeps working.
- Reserved, not built: external subcommands discovered as `wslkit-<name>.exe` next to
  the binary, for tools that must stay native C++ (wsldrive). wsldisk is expected to be
  reimplemented in Go inside the kit instead.
- JSON schema names move from `wsldoctor/...` to `wslkit/...`; `--from-snapshot` still
  loads the old names. The undo journal moves to `%LOCALAPPDATA%\wslkit\undo` and is
  shared by every subcommand.

## Why

Publishing and signing a dozen `wsl*` executables separately costs a manifest, a
review and a pipeline each; one kit costs one. More importantly the tools share most of
their code: config parsing, event log, WMI, VHDX, registry, redaction and the fix
journal are needed by nearly all of them. A single binary keeps one `--json` contract,
one exit-code contract and one elevation rule across the kit.

The rename happened while the repository was still private, so no public name changes.

## Consequences

- PLAN.md, ROADMAP.md and ARCHITECTURE.md remain the specification of the doctor
  feature and still say "wsldoctor" where they describe it; commands in them read as
  `wslkit doctor ...`.
- Release coupling: a bug in one subcommand ships with all; a broken subcommand build
  blocks the release. Accepted.
- Every mutating path in every subcommand follows the doctor's rule: explicit command,
  dry-run by default, `--apply`, journaled rollback.
