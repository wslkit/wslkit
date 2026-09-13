# ADR 0011 — `wslkit disk` absorbs wsldisk

Status: accepted, 2026-09-13.

## Decision

`wslkit disk` reimplements `wslkit/wsldisk` in Go inside the kit, and the
standalone C++ repository is archived once the parity checklist in
`docs/wsldisk-parity.md` is complete. ADR 0008 already reserved this.

The shape of the port:

- **Ports and adapters.** `internal/disk` holds pure logic: registrations, name
  resolution, preconditions, measurement composition, rendering. Windows lives
  behind five interfaces in `ports.go` (`Registry`, `FileSystem`, `Disks`,
  `Host`, `Clock`) whose implementations are the `_windows` files. This mirrors
  the seams wsldisk found necessary, for the same reason: a locked file, a full
  volume and a distribution that will not boot must all be reachable from a
  test.
- **`Clock` is a port** so the wait for the utility VM to release a disk is
  instant under test. That wait is tens of seconds of real time and would
  otherwise dominate the suite.
- **virtdisk.dll is bound by hand** in `internal/winapi/virtdisk`, late-bound
  through `LazyDLL`, no cgo, per ADR 0001. Parameter block layouts are pinned by
  tests because the kernel validates them by size and a wrong one fails at
  runtime with `ERROR_INVALID_PARAMETER`.
- **Exit codes extend the kit's.** 0, 2 and 3 already mean what wsldisk meant by
  them. `5` partial, `10` no such distribution and `11` busy are added with
  wsldisk's meanings, so scripts written against the old binary keep working.
- **JSON is built from maps**, so keys sort and absent stays absent. Every
  optional measurement is a pointer: a disk held open by a running distribution
  genuinely has no readable virtual size, and reporting `0` would be a lie
  rather than a gap.

## What changes from wsldisk

- **No self-elevation** (ADR 0003). wsldisk built a `ShellExecuteEx("runas")`
  relaunch with a named-pipe result channel, and never wired it to a flag; the
  code was unreachable. wslkit does not port it. Everything the disk commands do
  is measured to work unelevated; the one operation that needs a token, attaching
  a disk read-only, will require an already elevated console and say so.
- **No `--elevate` flag**, for the same reason.
- **No TOML.** wsldisk kept settings in `%APPDATA%\wsldisk\config.toml` via
  toml++. The dependency allow-list has no TOML parser and `wslkit disk config`
  will hand-write the small subset it needs, as `internal/wslconfig` already
  does for INI.
- **The undo journal is shared.** Mutating disk commands plan, apply and roll
  back through `internal/fix`, so `wslkit doctor undo` lists them alongside
  every other change the kit has made, rather than each subcommand inventing
  its own.

## Why not keep the C++

The tools share most of their platform layer. Registry reading, VHDX parsing,
volume queries, redaction and the fix journal already exist in the kit and were
duplicated in wsldisk. One binary means one signing identity, one release, one
`--json` contract and one exit-code contract. The measured risk in Go was the
virtual disk API without cgo, and the binding here compacts, reads sizes and
attaches with no C at all.

wsldrive is the counter-example and stays native: it is a filesystem driver
whose hot path is a metadata tree, and ADR 0008 reserves the external
subcommand mechanism for it.

## Consequences

- The `wsldisk` repository is archived, not deleted: its issues and its research
  document are cited from `docs/research/2026-09-subcommands.md`.
- Its packaging manifests (`wslkit.wsldisk` in winget, the Scoop manifest) are
  superseded by the kit's. A `wsldisk` shim name is **not** added: unlike
  `wsldoctor`, wsldisk was never published to a package manager.
- Commands land over several changes; `docs/wsldisk-parity.md` is the checklist
  and the archive gate.
