# wslkit

Tools for WSL 2 in one native Windows binary. The first tool is the doctor: it
diagnoses why WSL is broken or slow, then fixes it.

```
wslkit doctor                                      # read-only, no admin, ranked diagnosis
wslkit doctor explain "Wsl/Service/E_UNEXPECTED"   # decode an error, run the probes that explain it
wslkit doctor fix <id> [--apply]                   # explicit, per-action remediation with undo
wslkit doctor undo [<id>]                          # replay a rollback
```

> **Status: M1 of the doctor in progress, unreleased.** `wslkit doctor` runs end to end
> in about a second with 20 probes; fixes `update`, `shutdown`, `wslconfig` and
> `defender` plan and apply with an undo journal. Further tools (`disk`, `log`, `limit`,
> `proxy`) are planned as sibling subcommands; see [ADR 0008](docs/decisions/0008-wslkit-product.md).
> See **[PLAN.md](PLAN.md)** for the doctor's specification and probe catalog,
> **[ROADMAP.md](ROADMAP.md)** for phases, spike results and feature research,
> **[ARCHITECTURE.md](ARCHITECTURE.md)** for language, code layout, CI/CD and testing,
> and **[CONTRIBUTING.md](CONTRIBUTING.md)** to build it or donate a snapshot.

```
go build ./cmd/wslkit && .\wslkit.exe doctor
```

## Why a doctor

WSL fails in ways that are genuinely hard to diagnose. A real example, and the reason this
project exists:

```
Catastrophic failure
Error code: Wsl/Service/E_UNEXPECTED
```

That message meant a WSL runtime from early 2025 could not boot an Ubuntu 26.04 distro:
Ubuntu 26.04 is cgroup v2 only and runtimes before WSL 2.5 still mounted cgroup v1.
Nothing in the ecosystem checked runtime against distro, so finding it took a dozen
commands across the event log, the registry, the VHDX header and the WSL system distro.

`wslkit doctor` turns that into one line of output and one command to run, and
`wslkit doctor explain` decodes the error string itself using WSL's own source.

## Design

- **`doctor` never mutates.** Safe to run on a machine mid-crisis, safe to paste into a bug report.
- **No admin rights for `doctor`.** Probes degrade gracefully and say what needs `--elevated`.
- **Fixes are explicit.** No `--fix-all`; dry-run by default; every apply is journaled and undoable.
- **Every finding ends in an action.** A finding with no suggested command is a bug.
- **Links out rather than reimplementing.** Where a mature tool owns a problem, it gets recommended.
- **One kit.** Every subcommand shares the collectors, the `--json` contract, the exit codes and the undo journal.

## Guest agent

Some tools need a helper inside the distribution. `wslkit agent install -d <distro>` puts
one there (a static binary plus a systemd unit) and `wslkit agent start` runs the
Windows-side daemon it connects to over a Hyper-V socket. No admin rights on either side;
the daemon only opens targets you have allowed. See
[ADR 0009](docs/decisions/0009-guest-agent.md).

```
wslkit agent install -d Ubuntu --autostart
wslkit agent status
```

## Legacy name

The binary is multi-call: a copy or shim named `wsldoctor.exe` behaves as `wslkit doctor`.

## Part of wslkit

- [wsldisk](https://github.com/wslkit/wsldisk) — reclaim disk space from WSL 2 virtual disks (to become `wslkit disk`)
- [wsldrive](https://github.com/wslkit/wsldrive) — cross the WSL 2 filesystem boundary at native speed
- [skrog](https://github.com/wslkit/skrog) — the upstream Docker Engine on Windows via WSL 2

## Licence

TBD before first public release.
