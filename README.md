# wsldoctor

Diagnose why WSL2 is broken or slow, then fix it — one native Windows CLI.

```
wsldoctor check              # read-only, no admin rights, ranked diagnosis
wsldoctor fix <action>       # explicit, per-action remediation
```

> **Status: pre-development.** Nothing is implemented yet.
> See **[PLAN.md](PLAN.md)** for the full specification, probe catalog and milestones.

## Why

WSL fails in ways that are genuinely hard to diagnose. A real example, and the reason this
project exists:

```
Catastrophic failure
Error code: Wsl/Service/E_UNEXPECTED
```

That message means a WSL runtime from January 2025 cannot boot an Ubuntu 26.04
modern-format distro. Nothing in the ecosystem checks runtime-version against
distro-version compatibility, so finding it took a dozen commands across the event log,
the registry, the VHDX header and the WSL system distro — plus one wrong hypothesis.

`wsldoctor check` should turn that into one line of output and one command to run.

## Design

- **`check` never mutates.** Safe to run on a machine mid-crisis, safe to paste into a bug report.
- **No admin rights for `check`.** Probes degrade gracefully instead of failing.
- **Fixes are explicit.** No `--fix-all`; dry-run by default; rollback always printed.
- **Every finding ends in an action.** A finding with no suggested command is a bug.
- **Links out rather than reimplementing.** Where a mature tool owns a problem, it gets recommended.

## Part of wslkit

- [wsldrive](https://github.com/wslkit/wsldrive) — cross the WSL2 filesystem boundary at native speed
- [wsldisk](https://github.com/wslkit/wsldisk) — reclaim disk space from WSL2 virtual disks
- [skrog](https://github.com/wslkit/skrog) — the upstream Docker Engine on Windows via WSL2

## Licence

TBD before first public release.
