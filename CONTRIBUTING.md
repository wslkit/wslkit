# Contributing to wsldoctor

## Build and test

```
go build ./cmd/wsldoctor            # produces wsldoctor.exe on Windows
go test ./...                       # unit, snapshot and (on Windows) collector tests
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...
```

Probes are pure, so the snapshot suite also runs on Linux; CI does that on an Ubuntu
runner. From Windows you can at least compile-check it:
`GOOS=linux go test -c -o /dev/null ./internal/snapshottest`.

There is no Makefile on purpose; the commands above are the whole build.

## Where things live

| Path | What | Runs on |
|---|---|---|
| `internal/env` | the `Env` snapshot types (pure data) | anywhere |
| `internal/env/collect` | Windows collectors; the only code that reads the machine | Windows |
| `internal/winapi/*` | thin wrappers: WMI, event log, services, file info, processes | Windows |
| `internal/probe/*` | probes: pure functions `Env -> Result` | anywhere |
| `internal/fix`, `internal/fix/actions` | fix planning (pure); `internal/fix/exec` runs plans | anywhere / Windows |
| `internal/render`, `internal/redact` | human, JSON, report output | anywhere |
| `internal/data/files/*.json` | compat matrix, error dictionary; CI refreshes `latest_stable` weekly | anywhere |
| `tools/gen-errors`, `tools/gen-wslconfig-keys` | regenerate the source-derived parts of `errors.json` and `wslconfig-keys.json` from a microsoft/WSL checkout: `go run ./tools/gen-errors -wsl <dir>` | anywhere |
| `testdata/snapshots/<case>/` | one real environment per bug class, plus `expected.json` | anywhere |
| `docs/decisions/` | ADRs, including the Phase 0 spike results | |

Rule enforced by CI: anything under `internal/probe`, `internal/render`, `internal/redact`,
`internal/fix` (not `exec`), `internal/vhdx`, `internal/wslconfig`, `internal/wslver`,
`internal/data` must compile with `GOOS=linux` and must not import `golang.org/x/sys/windows`,
`go-ole`, `os/exec` or `internal/winapi`.

## Adding a probe

1. Pick the ID from PLAN.md §5 (or add a row there first).
2. If the probe needs a fact that `Env` does not carry yet, add the field to `internal/env/env.go`
   as a `Field[T]` and fill it in a collector. Record provenance in `Source`. Use
   `env.ErrNeedsElevation` when access is denied unelevated; never turn that into OK or FAIL.
3. Implement `probe.Probe` in the matching package. Every `FAIL`, `WARN` and `UNKNOWN`
   result must set `FixHint` (the snapshot test enforces this).
4. Register it in `internal/probe/all/all.go` after any probe it `Needs()`.
5. Add or extend a snapshot under `testdata/snapshots/` that exercises the new finding.

## Donating a snapshot

A snapshot is a real machine's `wsldoctor check --json` output. Redaction is on by
default: profile paths become `%USERPROFILE%` or `C:\Users\<user>`, SIDs and the hostname
are scrubbed. Check the file before sharing; then attach it to an issue or open a PR that
adds `testdata/snapshots/<short-name>/env.json`, `expected.json` and a one-paragraph
`README.md` saying what the machine was and what wsldoctor must detect.

## Fixes

Fixes emit a `Plan` (steps plus rollback) and never touch the machine while planning.
`wsldoctor fix <id>` prints the plan; `--apply` writes an undo journal entry to
`%LOCALAPPDATA%\wsldoctor\undo\` and then executes. `wsldoctor undo <id>` replays the
rollback. Test fixes with `fix.Recording`, never with the real executor
(`WSLDOCTOR_TEST=1` makes the real executor panic).

## Commits and releases

Conventional prefixes help the release notes: `probe:`, `fix:`, `collect:`, `render:`,
`data:`, `docs:`, `ci:`. Tag `vX.Y.Z` to release; goreleaser builds amd64 and arm64
Windows binaries with SBOM and provenance attestation.
