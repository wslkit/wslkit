# ADR 0003 — No self-elevation in v1

Status: accepted, 2026-09-12.

`check` never elevates. Collectors detect whether the process is already elevated and
read what they can; fields that need admin record `err_kind: needs_elevation` and the
matching probes report `UNKNOWN` with the hint `wslkit doctor check --elevated`.

`check --elevated` and any `fix` with `Elevates() == true` require an already elevated
console and exit with code 2 otherwise. No `ShellExecute("runas")` relaunch.

Why: a relaunch breaks stdout capture for `--json` consumers, confuses exit codes, and
presents a UAC prompt the user cannot audit. Revisit after M2 if support load says so.
