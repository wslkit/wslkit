# ADR 0004 — Snapshot-first testing and Linux-enforced probe purity

Status: accepted, 2026-09-12.

1. All Windows I/O happens in `internal/env/collect` (build-tagged `windows`) and
   `internal/winapi`. Everything under `internal/probe`, `internal/render`,
   `internal/redact`, `internal/fix` (planning), `internal/vhdx`, `internal/wslconfig`,
   `internal/wslver`, `internal/wslerr` is pure and must compile and test with
   `GOOS=linux`. CI enforces this; `.golangci.yml` `depguard` forbids the Windows
   imports there.
2. `wsldoctor check --json` output is the fixture format. `check --from-snapshot env.json`
   re-runs every probe on a saved environment. Fixtures live in `testdata/snapshots/<case>/`
   with `env.json`, `expected.json` (top findings, lower-bound confidence) and `README.md`.
3. Collectors are tested for behaviour (no panic, provenance filled, deadline honoured),
   not for values, because hosted runners are not stable machines.

Why: you cannot CI a broken WSL, but you can CI a JSON file describing one.
