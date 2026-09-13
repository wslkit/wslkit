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

## Bridged sockets

`wslkit sock` gives a distribution the Windows SSH and GPG agents, so keys stay on
Windows (including hardware-backed ones) and Linux tools use them unchanged.

```
wslkit sock list
wslkit sock enable ssh-agent -d Ubuntu     # SSH_AUTH_SOCK, via the Windows OpenSSH or 1Password agent
wslkit sock enable gpg-agent -d Ubuntu     # gpg4win, including its SSH support
wslkit sock status -d Ubuntu
```

## Disks

`wslkit disk` reports what each distribution costs on disk, and reclaims what it
can. Reporting reads only: nothing is started and nothing is changed.

```
wslkit disk list                 every distribution, its size on disk and what is reclaimable
wslkit disk list --json          integer bytes, one object per line
wslkit disk info Ubuntu          registration, disk geometry and guest usage
wslkit disk info Ubuntu --probe  start it if stopped, to read the usage inside
```

Reclaiming space is two steps: the guest has to discard the blocks it no longer
uses, then the disk file itself is shrunk. `compact` does both.

```
wslkit disk trim Ubuntu                  ask the guest to discard what it no longer uses
wslkit disk compact Ubuntu               trim, stop, then shrink the file
wslkit disk compact --all --shutdown     every distribution, stopping WSL to free the disks
wslkit disk compact --file D:\d.vhdx     a loose disk, such as the one Docker Desktop keeps
wslkit disk compact Ubuntu --dry-run     the plan, having changed nothing
```

No elevation is needed for any of this.

To find out what is using the space in the first place:

```
wslkit disk usage Ubuntu                       catalogued caches, biggest first
wslkit disk usage Ubuntu --by-directory        plus a breakdown of the whole guest
wslkit disk usage Ubuntu --top 10 --json       the ten largest, as integer bytes
```

`usage` only reports. It never deletes anything, and a row marked not clearable
means wslkit cannot judge whether what it holds still matters, not that removing
it is dangerous.

When a disk has been moved by hand, or a distribution was removed and its disk
was not:

```
wslkit disk orphans                            .vhdx files no distribution claims
wslkit disk orphans --scan D:\wsl               look somewhere else as well
wslkit disk orphans --delete                   after one confirmation for the whole set
wslkit disk relink Ubuntu D:\wsl\ext4.vhdx      repoint a distribution at its disk
```

Unclaimed is not the same as unused: Docker Desktop keeps a disk holding every
volume you have and no distribution claims it. `orphans` says so before it
deletes anything, refuses any file that is open, and treats end of input as a
no. `relink` writes registry values only, starts the distribution to check the
new path works, and puts the registry back if it does not.

Settings live in `%APPDATA%wslkitnfig.toml`:

```
wslkit disk config                             what is set, and where
wslkit disk config set compact.trim false      change one setting
wslkit disk config get compact.trim            the bare value, for scripts
wslkit disk config edit                        open it in $EDITOR
```

A flag given on the command line always wins over the setting.

Tab completion for every command, generated from the command tree so it cannot
drift:

```
wslkit completion powershell | Out-String | Invoke-Expression
source <(wslkit completion bash)
source <(wslkit completion zsh)
```

Distribution names are resolved when you press Tab, so a script generated last
month knows about a distribution installed this morning.

`wsl --unregister` deletes the disk along with the registration, without asking,
and fires its notification only afterwards, so nothing can intercept it. `trash`
is a wrapper that makes it survivable:

```
wslkit disk trash Ubuntu           unregister it, but keep the disk
wslkit disk undelete Ubuntu        register it again, with its settings
wslkit disk trash --list           what is in the trash, and how old
wslkit disk trash --purge --older-than 30d   free the space for good
```

It stops the distribution, writes down everything the registration says, moves
the disk to `%LOCALAPPDATA%\wslkit\trash`, and only then unregisters, so WSL
finds nothing left to delete. `undelete` imports the disk where it lies and puts
back the default user, the flags and its place as the default distribution.

To put a disk on another drive:

```
wslkit disk move Ubuntu D:\wsl                 move it, then check it still boots
wslkit disk move Ubuntu D:\wsl --keep-source   copy it and leave the original
wslkit disk move Ubuntu D:\wsl --dry-run       the plan, having changed nothing
```

The copy preserves the holes in the disk, so a 12 GiB file stays 12 GiB rather
than becoming the terabyte it is nominally allowed to reach. The original is
deleted last, and only once the distribution has started from its new home; if
it does not start, everything is put back.

This replaces [wsldisk](https://github.com/wslkit/wsldisk), which is archived.
What was ported, and the three places this version deliberately differs, are in
[docs/wsldisk-parity.md](docs/wsldisk-parity.md) and [ADR 0011](docs/decisions/0011-disk-subcommand.md).

## Legacy name

The binary is multi-call: a copy or shim named `wsldoctor.exe` behaves as `wslkit doctor`.

## Part of wslkit

- [wsldrive](https://github.com/wslkit/wsldrive) — cross the WSL 2 filesystem boundary at native speed
- [skrog](https://github.com/wslkit/skrog) — the upstream Docker Engine on Windows via WSL 2

## Licence

[MIT](LICENSE), matching `wsldrive` and `skrog`.
