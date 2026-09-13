# wsldisk parity checklist

`wslkit/wsldisk` is archived once every row below is done. Until then it stays
open and this file is the gate. See ADR 0011 for how the port is built and what
it deliberately changes.

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
| `wsldisk move <distro> <dir>` | `wslkit disk move <distro> <dir>` | todo |
| `wsldisk relink <distro> <path>` | `wslkit disk relink <distro> <path>` | **done** |
| `wsldisk config [path\|get\|set\|edit]` | `wslkit disk config ...` | todo |
| `wsldisk completion <shell>` | `wslkit completion <shell>` | todo, kit-wide rather than disk-only |

Not ported, deliberately: `--elevate` and the elevated worker, which were never
wired to a flag in wsldisk and which ADR 0003 rules out.

## Flags

| Flag | Commands | Status |
|---|---|---|
| `--json` | all but `completion` | **done** for list, info |
| `--verbose`, `-v` | all | **done** for list, info |
| `--dry-run` | all | **done** for list, info, trim, compact, usage |
| `--yes`, `-y` | all | **done** |
| `--log FILE` | all | todo |
| `--probe` | `list`, `info` | **done** |
| `--top`, `--by-directory`, `--depth` | `usage` | **done** |
| `--all`, `--file`, `--no-trim`, `--restart`, `--shutdown` | `compact` | **done**, plus `--unlock-timeout` and `--trim-timeout` |
| `--scan`, `--delete`, `--relink`, `--to` | `orphans` | **done** |
| `--keep-source` | `move` | todo |

## Contracts

| Item | Status |
|---|---|
| Exit codes 0, 2, 3, 5, 10, 11 | **done**, all six now reachable |
| JSON: sizes as integer bytes, sorted keys, absent never zero | **done** |
| JSON: one object per line, not an array | **done** |
| Errors as a JSON object on stdout under `--json` | todo |
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
- [ ] `move` deletes the source only after the distribution has booted from the
      copy.
- [x] A plan may not schedule an undoable change after an irreversible step.
- [x] `orphans` re-checks the extension in code, because the filesystem matches
      `*.vhdx` against short names too.
- [x] `orphans --delete` asks once for the whole set, and end-of-input is a no.
- [x] Docker Desktop keeps a disk no distribution claims; deleting it is not
      safe merely because it is unclaimed.

## Repository

- [x] The cache catalogue is carried over and embedded, as JSON rather than TOML.
- [ ] README and site pages cover every disk subcommand.
- [ ] `wsldisk` is archived and its README points at wslkit.
