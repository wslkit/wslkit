# ADR 0001 — Language and dependency allow-list

Status: accepted, 2026-09-12.

## Decision

Go (1.27 at time of writing, pinned in `go.mod`), no cgo, single static binary for
`windows/amd64` and `windows/arm64`.

Allowed third-party modules:

| Module | Purpose | Why not stdlib |
|---|---|---|
| `golang.org/x/sys` | registry, Service Control Manager, tokens, file version info, `wevtapi` | not in stdlib |
| `github.com/go-ole/go-ole` | late-bound COM for three WMI queries | no stdlib COM |

Anything else needs a new ADR. CLI is `flag` plus a small router. JSON, INI parsing,
VHDX parsing are hand-written in `internal/`.

## Why

See ARCHITECTURE.md §1. The spike in ADR 0007 confirmed the only risk (WMI from Go
without PowerShell) works unelevated.
