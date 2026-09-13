# wsldisk parity checklist

This was the gate for archiving `wslkit/wsldisk`, the C++ tool that `wslkit disk`
replaces. See ADR 0011 for how the port is built and what it deliberately
changes.

**Status: complete.** Every command and flag is ported, and `wslkit/wsldisk` was
archived on 2026-09-13. Its README, its repository description and a pinned
issue point here. This file is kept as the record of what was ported and why.

The C++ repository stays readable: `docs/RESEARCH.md` there holds measurements
that are cited from this repository and not repeated in it.

The reference is wsldisk at commit `a0d60c5`.

## Commands

| wsldisk | wslkit | Status |
|---|---|---|
| `wsldisk list` | `wslkit disk list` | **done** |
| `wsldisk info <distro>` | `wslkit disk info <distro>` | **done** |
| `wsldisk trim <distro>` | `wslkit disk trim <distro>` | **done** |
| `wsldisk compact [distro]` | `wslkit disk compact [distro]` | **done** |
| `wsldisk usage <distro>` | `wslkit disk usage <distro>` | **done** |
| `wsldisk orphans` | `wslkit disk orphans` | **done** |
| `wsldisk move <distro> <dir>` | `wslkit disk move <distro> <dir>` | **done** |
| `wsldisk relink <distro> <path>` | `wslkit disk relink <distro> <path>` | **done** |
| `wsldisk config [path|get|set|edit]` | `wslkit disk config ...` | **done** |
| `wsldisk completion <shell>` | `wslkit completion <shell>` | **done**, kit-wide rather than disk-only |

Not ported, deliberately: `--elevate` and the elevated worker, which were never
wired to a flag in wsldisk and which ADR 0003 rules out.

## Flags

| Flag | Commands | Status |
|---|---|---|
| `--json` | all but `completion` | **done** for list, info |
| `--verbose`, `-v` | all | **done** for list, info |
| `--dry-run` | all | **done** for list, info, trim, compact, usage |
| `--yes`, `-y` | all | **done** |
| `--log FILE` | all | **done** |
| `--probe` | `list`, `info` | **done** |
| `--top`, `--by-directory`, `--depth` | `usage` | **done** |
| `--all`, `--file`, `--no-trim`, `--restart`, `--shutdown` | `compact` | **done**, plus `--unlock-timeout` and `--trim-timeout` |
| `--scan`, `--delete`, `--relink`, `--to` | `orphans` | **done** |
| `--keep-source` | `move` | **done** |

## Settings

`%APPDATA%wslkitnfig.toml`, written by `wslkit disk config set` and safe to
edit by hand. Unknown keys are ignored so a file from a later version still
loads.

| Key | Default | Notes |
|---|---|---|
| `scan.dirs` | empty | Extra `orphans` roots, semicolon-separated when set |
| `compact.trim` | `true` | |
| `compact.restart` | `false` | |
| `wsl.unlock_timeout_seconds` | `90` | At most 3600 |

One deliberate improvement on wsldisk: a flag actually given on the command line
wins over the setting in either direction. wsldisk folded them one-way, so a
config that turned trimming off could not be overridden from the CLI at all.

## Contracts

| Item | Status |
|---|---|
| Exit codes 0, 2, 3, 5, 10, 11 | **done**, all six now reachable |
| JSON: sizes as integer bytes, sorted keys, absent never zero | **done** |
| JSON: one object per line, not an array | **done** |
| Errors as a JSON object on stdout under `--json` | **done**, with the stable token |
| Human table: two-space gutter, `-` for unmeasurable, no trailing space | **done** |
| `format_size` truncates rather than rounds | **done** |
| Widths counted in code points | **done** |

## Behaviour worth keeping

Each of these cost wsldisk a bug report. They are ported as written, with the
reason in a comment next to the code.

- [x] Measuring never starts a distribution unless `--probe` says so.
- [x] `du -d` rather than `--max-depth`, which busybox rejects.
- [x] `~` expands against real home directories only, never service accounts.
- [x] Overlapping catalogue entries are counted once, so the total never claims
      more space than the guest is using.
- [x] The copy is sparse-aware, so moving a disk does not write out its holes.
- [x] A cross-volume rename is never attempted: its fallback would fill the
      holes in.
- [x] `df` columns are counted from the right, never by header text, so a
      localised guest and a wrapped device name both parse.
- [x] A registration with no `DistributionName` or no `BasePath` is a warning
      and a skipped row, not a failed command.
- [x] `BasePath` keeps its `\\?\` prefix, or its absence, exactly as stored.
- [x] Size on disk comes from the compressed-size query, not the logical length.
- [x] Sparseness comes from the file attribute, never from the `Flags` value.
- [x] A missing disk produces one actionable note, not one failure per
      measurement that reads it.
- [x] The compaction open uses the version 2 parameter block with a zero access
      mask, so a wrong mask fails at open rather than mid-compaction.
- [x] `wsl.exe` output is UTF-8 from the guest but UTF-16 for its own
      diagnostics; both are decoded.
- [x] `fstrim -v` falls back to `fstrim` when busybox rejects the flag.
- [x] The figure fstrim reports is the free extent, not space reclaimed, and is
      labelled as such.
- [x] Compaction waits for the utility VM to release the disk, and says which
      of the three reasons it could not.
- [x] The smoke test after a move or relink is `/bin/sh -c :`, because NixOS-WSL
      has no `/bin/true`.
- [x] `move` deletes the source only after the distribution has booted from the
      copy.
- [x] A plan may not schedule an undoable change after an irreversible step.
- [x] `orphans` re-checks the extension in code, because the filesystem matches
      `*.vhdx` against short names too.
- [x] `orphans --delete` asks once for the whole set, and end-of-input is a no.
- [x] Docker Desktop keeps a disk no distribution claims; deleting it is not
      safe merely because it is unclaimed.

- [x] Settings are read from a file, with the four keys wsldisk had.
- [x] An unparseable settings file is a warning everywhere except `config`
      itself, which fails, because that is what the user came to look at.

- [x] Completion is generated from the command tree, never hand-written, and a
      test fails if the tree and the usage text disagree.
- [x] Distribution names are resolved when the user presses Tab, not baked into
      the script.

## Repository

- [x] The cache catalogue is carried over and embedded, as JSON rather than TOML.
- [x] The README covers every disk subcommand. Site pages are tracked separately
      in the documentation site issue.
- [x] `wsldisk` is archived and its README points at wslkit.
