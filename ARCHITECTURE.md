# wsldoctor — Language, code architecture, CI/CD and testing

> Companion to [PLAN.md](PLAN.md) (what) and [ROADMAP.md](ROADMAP.md) (when). This is the
> engineering design. Status: **ratified and implemented as the Phase 1 skeleton on
> 2026-09-12**; see `docs/decisions/` (ADR 0001–0007). Deviations from this text in the
> code are noted inline below. Embedded data lives in `internal/data/files/` (Go's `embed`
> cannot reach a top-level `data/`).

## 1. Language

### Decision: Go (**ratify**)

| Option | For | Against | Verdict |
|---|---|---|---|
| **Go** | Single static binary, no runtime. `golang.org/x/sys/windows` covers registry, Service Control Manager, `wevtapi` (`EvtQuery`/`EvtNext`/`EvtRender`), tokens/elevation, file attributes. `github.com/Microsoft/go-winio/vhd` wraps `virtdisk.dll`. Matches `skrog`. Native fuzzing and `embed`. Cross-compiles from Linux without cgo, so most CI runs on cheap Linux runners. | COM/WMI needs `go-ole` and manual apartment handling. Error handling is verbose. | **Choose.** The only real risk (WMI) is retired by spike S1 in ROADMAP. |
| Rust + `windows-rs` | Best Win32/COM coverage of any language, typed WMI via `windows::Win32::System::Wmi`. | Nobody else in wslkit uses it; slower iteration on a probe-and-report tool; compile times. | Reject for now. Revisit only if S1 fails. |
| C# / .NET NativeAOT | `System.Management` makes WMI trivial. | `System.Management` is not AOT-compatible; without AOT the binary needs a runtime, and the wslkit rule is single-file native. | Reject. |
| C++ (as `wsldisk`) | Zero-friction Win32. | No benefit for non-hot-path logic; testing and JSON are painful; slows contributors. | Reject; PLAN §4 already says so. |

Toolchain: current stable Go (1.26 or newer), pinned in `go.mod`; `GOFLAGS=-trimpath`,
`-ldflags "-s -w -X main.version=..."`. Targets: `windows/amd64`, `windows/arm64`.
No cgo anywhere; if a spike proves cgo unavoidable the decision comes back here.

Dependencies, kept deliberately short (**ratify** each addition beyond this list):

- `golang.org/x/sys/windows` — registry, SCM, wevtapi, security, tokens.
- `github.com/go-ole/go-ole` — COM for WMI (`SWbemLocator`, late-bound).
- `github.com/Microsoft/go-winio` — `vhd` package; only if the pure VHDX parser (§3.7) proves insufficient.
- Standard library for CLI (`flag` + a 40-line subcommand router), JSON, INI parsing (own, §3.8), testing.

No cobra, no viper, no logrus. A diagnostic tool people run once should start in
milliseconds and have a dependency graph a reviewer can read in one sitting.

## 2. Architectural principles (derived from PLAN §3)

1. **Probes are pure functions of `Env`.** They never touch Windows APIs. All I/O happens
   in collectors, once, before any probe runs. This is what makes the snapshot test
   strategy (§5) possible and makes `check` provably read-only.
2. **Every collected value carries provenance.** `Field[T]{Value T; Source string; Err error}`
   distinguishes "absent" from "could not read" from "needs elevation". Probes turn the
   last two into `Unknown`, never into `OK` or `Fail`.
3. **Nothing wakes the VM without consent.** `wsl.exe` is invoked only by collectors behind
   `--allow-vm-wake`, with a hard deadline, in a separate process group so a hung child can
   be killed.
4. **Fixes go through an `Executor`.** No fix calls `os/exec`, the registry or the
   filesystem directly; it emits operations that a real or recording executor performs.
   Dry-run is the recording executor printing its log.
5. **Data is data.** Compat matrix, config key table, error dictionary are embedded JSON
   with a schema, updated by CI, never hand-edited Go.

## 3. Code layout

```
cmd/wsldoctor/            main.go: version, subcommand router, exit codes
internal/cli/             check.go, fix.go, explain.go, preflight.go, undo.go, flags.go
internal/env/             Env, Field[T], sub-structs (Runtime, Host, Distros, Config, Disk, Net, Events)
internal/env/collect/     Collector interface + Windows collectors (//go:build windows)
internal/probe/           Probe interface, Result, Status, registry, ranking, dependency skip
internal/probe/wsl/       WSL001–WSL007
internal/probe/host/      HST***, DEF***, PLG***, HIB/PWR***
internal/probe/disk/      DSK***, ZON***
internal/probe/net/       NET***
internal/probe/perf/      MEM***
internal/probe/evt/       EVT***
internal/fix/             Fix interface, Plan, Executor (real, recording), undo journal
internal/fix/actions/     update, defender, zone, oobe, shutdown, wslconfig
internal/render/          human.go, json.go, report.go (markdown), width-aware wrapping
internal/redact/          path/SID/hostname/GUID scrubbing applied to a Result tree
internal/data/            embed.go + loaders + schema validation for data/*.json
internal/winapi/          thin, tested wrappers: registry, scm, wevtapi, wmi, virtdisk, iphlpapi, token
internal/vhdx/            pure VHDX header/region/metadata/BAT parser (no Windows deps)
internal/wslconfig/       pure INI parser + lint rules + key table lookup
internal/wslerr/          parser for "Wsl/Service/…/E_FOO" paths and HRESULTs
internal/wslver/          version parse/compare, channel (stable vs pre-release) detection
data/                     compat.json, wslconfig-keys.json, errors.json, refs.json
schema/                   env-v1.json, result-v1.json (JSON Schema, published with releases)
testdata/snapshots/       <case>/env.json + expected.json + expected.txt
testdata/vhdx/            tiny hand-built VHDX fixtures (headers only, a few KB)
spikes/                   Phase 0 throwaway programs; deleted after docs/decisions/ records the answer
docs/decisions/           ADR-000x-*.md
```

Packages under `internal/probe/*`, `internal/vhdx`, `internal/wslconfig`, `internal/wslerr`,
`internal/wslver`, `internal/render`, `internal/redact` **must build and test on Linux**.
CI enforces this with `GOOS=linux go test ./...` on those paths; it is the guardrail that
keeps probes pure.

### 3.1 `Env`

```go
type Field[T any] struct {
    Value  T
    Source string // "HKCU\\...\\Lxss\\{guid}\\Modern", "Win32_OptionalFeature", "wsl.exe --version"
    Err    error  // nil | ErrNotPresent | ErrNeedsElevation | ErrVMWakeRefused | other
}

type Env struct {
    SchemaVersion int
    CollectedAt   time.Time
    Elevated      bool
    Host    Host      // OS build, edition, arch, hypervisor present, features, services, RAM
    Runtime Runtime   // installed version, channel, flavor (store/inbox/msi), appx identity
    Distros []Distro  // one per Lxss GUID: registry values + vhdx facts + running state
    Config  Config    // raw .wslconfig text + parsed + per-distro wsl.conf when readable
    Net     Net       // adapters, HNS networks, VPN heuristics, IPv6 state, firewall state
    Defender Defender // exclusions (or ErrNeedsElevation), realtime state, MDE plugin presence
    Events  Events    // last N relevant events per channel, already filtered
    Procs   Procs     // vmmem/vmmemWSL working set, wslservice state
    Plugins []Plugin  // HKLM Lxss\Plugins entries with file existence/version
}
```

Collectors run concurrently with a shared 10 s budget; each field has its own deadline.
A collector that times out records `Err` and the run continues. The full `Env` is what
`--json` emits and what `--from-snapshot` reads back.

### 3.2 Probes

```go
type Probe interface {
    ID() string
    Title() string
    Milestone() string          // "M1"…"M3", drives --only
    Needs() []string            // probe IDs whose Fail should cascade to Skipped here
    Run(env *env.Env) Result    // pure; no ctx because no I/O
}
```

Ranking: `Fail` before `Warn` before `Unknown` before `Skipped` before `OK`; within a
status by `Confidence` descending, then ID. `Needs()` lets `WSL002 = not installed`
collapse forty probes into one line instead of forty red lines.

The registry is an explicit slice in `internal/probe/all.go`, not `init()` side effects,
so the set of probes is greppable and deterministic.

### 3.3 Fixes

```go
type Fix interface {
    ID() string
    Elevates() bool
    Plan(env *env.Env, opts Options) (Plan, error)   // pure
}
type Plan struct { Steps []Step; Rollback []Step; Warnings []string }
type Executor interface { Run(Step) error }         // RealExecutor, RecordingExecutor
```

`fix <id>` = `Plan` → print → if `--apply`: write undo journal entry → `RealExecutor`.
`undo <journal-id>` replays `Rollback`. Journal lives in `%LOCALAPPDATA%\wsldoctor\undo\`.

### 3.4 Elevation

`check` never elevates. `check --elevated` and `fix defender` **require** an already
elevated console and exit with a clear message otherwise. No self-relaunch via `runas`
in v1: it complicates stdout capture, confuses `--json` consumers, and is a UAC prompt
users cannot audit. Revisit after M2 if support load says so.

### 3.5 WMI

One helper: `wmi.Query(ctx, namespace, wql string) ([]map[string]any, error)`. Runs on a
locked OS thread with its own `CoInitializeEx(COINIT_MULTITHREADED)`, late-bound through
`SWbemLocator.ConnectServer` and `ExecQuery`. Used by exactly three collectors: optional
features (`Win32_OptionalFeature`), hypervisor presence (`Win32_ComputerSystem`), Defender
(`root/microsoft/windows/defender` `MSFT_MpPreference`, if S3 allows). Everything else is
direct Win32.

### 3.6 Event log

`wevtapi` via x/sys: `EvtQuery` with an XPath filter over the last N minutes on
`Microsoft-Windows-Hyper-V-VmSwitch/Operational`, `Microsoft-Windows-Hyper-V-Compute-Admin`,
`Microsoft-Windows-Host-Network-Service/Operational`, and `Application` filtered to
`Windows Error Reporting` / `Application Error` with `wslservice.exe` or `wsl.exe`.
Render to XML, parse the few fields needed. Read-only, unelevated for these channels
(to be confirmed by S4; anything that is not becomes `ErrNeedsElevation`).

### 3.7 VHDX

Pure parser in `internal/vhdx`: file identifier at 0 (`vhdxfile`), two headers at 64 KiB
and 128 KiB (pick the higher sequence number with valid CRC-32C), region table at
192 KiB, metadata region → virtual disk size, block size, logical sector size; Block
Allocation Table → count of `PAYLOAD_BLOCK_FULLY_PRESENT` entries → allocated bytes.
That gives `DSK001` "file size vs allocated vs virtual size" without mounting or admin.
Fixtures are synthetic: headers plus an empty BAT, a few KiB each, generated by a test
helper so the repo carries no real disk images. `go-winio/vhd` is the fallback if
reading the BAT of a live, attached VHDX proves unreliable.

### 3.8 `.wslconfig` parsing

Own INI reader that preserves line numbers and raw text (needed for lint messages and
the `fix wslconfig` comment-out step). Key table from `data/wslconfig-keys.json`:
section, key, type, default, `min_windows_build`, `min_wsl`, `deprecated_in`,
`moved_from` (for `[experimental]` graduation). Lint rules are small functions
`func(cfg *Config, table *KeyTable, host env.Host) []Finding`, table-tested.

### 3.9 Output contracts

- Human: 100-column wrap, no colour when not a TTY or `NO_COLOR` set, status words not
  glyphs (people paste this into issues; emoji breaks in some renderers).
- `--json`: `{ schema: "wsldoctor/result/v1", env: <Env>, results: [...] }`. Additive
  changes only within `v1`; a breaking change bumps to `v2` and both are emitted for one
  minor release.
- `--report`: markdown block, redacted, with the tool version, `Env` summary, ranked
  findings, and a footer pointing to `collect-wsl-logs.ps1` for escalation.
- Exit codes: `0` no Fail, `1` at least one Fail, `2` usage error, `3` collector failure
  severe enough that results are unreliable (e.g. registry unreadable).

## 4. CI/CD

### 4.1 Pipelines

| Workflow | Trigger | Runner | Steps |
|---|---|---|---|
| `ci.yml` | push, PR | `ubuntu-latest` | `go vet`, `staticcheck`, `golangci-lint`, `govulncheck`, `GOOS=linux go test` on pure packages, `GOOS=windows GOARCH=amd64/arm64 go build` (cross-compile, no cgo), schema validation of `data/*.json`, snapshot corpus tests. Fast path: under 3 minutes. |
| `ci-windows.yml` | push, PR | `windows-latest`, `windows-11-arm` | `go test ./...` with `-tags integration` for `internal/winapi` and `internal/env/collect`; live `wsldoctor check --json` smoke; validate output against `schema/result-v1.json`; upload the produced `env.json` as an artifact. |
| `nightly.yml` | cron daily | `windows-latest`, `windows-11-arm` | Live `check` on the fresh runner image; diff `env.json` against the previous night; open an issue on unexpected change (runner image drift is a free early-warning for Windows changes). Also `go test -fuzz` for 10 minutes per fuzz target. |
| `data-refresh.yml` | cron weekly | `ubuntu-latest` | Fetch GitHub releases API and `wsl-config.md`; regenerate `data/*.json`; open a PR if the diff is non-empty. Humans merge. |
| `release.yml` | tag `v*` | `ubuntu-latest` build, `windows-latest` sign | goreleaser: zip + `.exe` for amd64/arm64, `checksums.txt`, SBOM (syft, SPDX), SLSA provenance via `actions/attest-build-provenance`; Authenticode signing; GitHub Release with generated notes; Scoop manifest PR to the wslkit bucket; winget PR via `winget-releaser`. |

Hosted Windows runners **do not officially support nested virtualization**, so WSL2
itself cannot be exercised in CI. The live smoke therefore always produces a
"virtualization unavailable / no distros" snapshot. That is fine: it is a legitimate
fixture (it must render `HST003 FAIL` first and nothing else red), and it proves the
collectors do not crash on a machine without WSL. Real WSL2 end-to-end runs happen on a
self-hosted runner if one is ever donated, otherwise on the manual pre-release matrix (§5.7).

### 4.2 Code signing (**ratify**)

Unsigned binaries trigger SmartScreen and Defender heuristics, and this tool touches
Defender exclusions, which is exactly the behaviour heuristics flag. Options:
**SignPath Foundation** (free for OSS, policy-gated, well understood by goreleaser users)
or **Azure Trusted Signing** (cheap, needs an Azure tenant with a verified identity).
Recommend SignPath for a public OSS project. Sign every release binary; publish the
certificate thumbprint in the README.

### 4.3 Supply chain

- Dependabot for Go modules and Actions, weekly, grouped.
- `govulncheck` in CI; `go.sum` committed; `GOFLAGS=-mod=readonly`.
- Actions pinned by SHA. Release workflow has `id-token: write` only.
- Reproducible builds: `-trimpath`, fixed `-buildvcs=false`, goreleaser `mod_timestamp`.
  A `verify-reproducible` job rebuilds from the tag and compares hashes.

### 4.4 Versioning and channels

SemVer. `v0.x` until M2 ships. `main` always releasable; features land behind
`Milestone()` gating so half-built probes can merge without appearing in `check`.
Nightly builds attached to a rolling `nightly` pre-release for people who volunteer
snapshots.

## 5. Testing

### 5.1 The pyramid, bottom up

| Layer | What | Where it runs | Size target |
|---|---|---|---|
| Unit, pure | version compare, error-path parser, INI parser, VHDX parser, lint rules, ranking, redaction | Linux and Windows | hundreds of table-driven cases |
| Fuzz | `wslconfig.Parse`, `wslerr.Parse`, `vhdx.ParseHeader`, `wslver.Parse` | nightly, 10 min each | crash-free; corpus committed |
| Snapshot (golden) | every probe against `testdata/snapshots/*/env.json` → `expected.json` and `expected.txt` | Linux and Windows | one snapshot per real bug class; founding bug is `snapshots/ubuntu-2604-on-2.4.13/` |
| Collector integration | each Windows collector against the live runner: succeeds, populates `Source`, never panics, honours deadline | Windows runners, `-tags integration` | one test per collector |
| Fix planning | `Plan()` against snapshots; `RecordingExecutor` asserts exact steps and rollback; undo round-trip | Linux and Windows | one per fix, plus "refuses without --apply" |
| Contract | `--json` validates against `schema/result-v1.json`; human output golden files at 80 and 120 columns | both | per renderer |
| Live smoke | `wsldoctor check --json` exit code and schema on hosted runners | Windows runners | one |
| Manual matrix | real WSL2 on real machines before each release | humans | §5.7 |

### 5.2 Snapshots are the product's test suite

A snapshot directory is:

```
testdata/snapshots/ubuntu-2604-on-2.4.13/
  env.json        # exactly what `check --json` emitted, redacted
  expected.json   # ordered list of {id, status, confidence_min, fix_id}
  expected.txt    # golden human rendering (regenerate with -update)
  README.md       # one paragraph: where it came from, issue link, what it must detect
```

Rules: `expected.json` asserts order of the top three findings, not the whole list, so
adding a probe does not break forty fixtures. Confidence is asserted as a lower bound.
Redaction is verified by a test that greps every `env.json` for `C:\Users\` and SIDs.

Provenance of snapshots: the maintainers' machines first; then donated via `--report`
(the footer asks for `wsldoctor-env.json`); then CI runners. A snapshot from a public
issue links back to it in `README.md`.

### 5.3 Probe purity is enforced, not requested

`internal/probe/...` is compiled with `GOOS=linux` in CI. Any import of
`golang.org/x/sys/windows`, `go-ole`, `os/exec` or `internal/winapi` fails the build
there. A `depguard` rule in golangci-lint gives a friendlier message than the linker.

### 5.4 Collectors are tested for behaviour, not values

A hosted runner's registry contents are not stable, so collector tests assert shape:
no panic, `Source` filled, `Err` is one of the known sentinel errors or nil, deadline
respected (a collector given a 1 ms context returns within 100 ms with
`context.DeadlineExceeded`). Value assertions live in the snapshot layer.

### 5.5 Fixes are tested without touching the machine

`RecordingExecutor` captures `Step`s as structured data. Tests assert, for example, that
`fix defender` plans exactly the exclusion paths present in `Env`, marks itself
`Elevates() == true`, and that `Rollback` removes exactly what `Steps` added. A test
also asserts that `fix` with no `--apply` never constructs a `RealExecutor`
(a build-tag-guarded panic in `RealExecutor` when `WSLDOCTOR_TEST=1`).

### 5.6 Property and differential checks

- `wslver`: `Compare(a,b) == -Compare(b,a)`; parsing then formatting round-trips.
- `vhdx`: any header the generator emits parses back to the same struct; corrupted CRC
  is detected; header selection picks the higher sequence number.
- `wslconfig`: lint on a file with zero unknown keys against the current key table yields
  zero unknown-key findings for every documented example in `wsl-config.md`
  (the examples are vendored as fixtures and refreshed by `data-refresh.yml`).

### 5.7 Manual pre-release matrix

Kept in `docs/release-checklist.md`; each cell is a `check --report` pasted into the
release PR.

| Host | WSL runtime | Distro | Expected headline |
|---|---|---|---|
| Windows 10 22H2 (19045) | latest stable | Ubuntu 24.04 (appx) | all OK, or DEF001 WARN |
| Windows 11 24H2 | latest stable | Ubuntu 26.04 (.wsl) | all OK |
| Windows 11 24H2 | pinned 2.4.13 (MSI) | Ubuntu 26.04 (.wsl) | WSL001 FAIL first |
| Windows 11 arm64 | latest stable | any | all OK; arm64 binary |
| Windows 11 24H2 | latest stable | none installed | WSL004 WARN, nothing else red |
| Windows 11 24H2, Hyper-V disabled | latest stable | any | HST001/HST003 FAIL first |
| Windows 11 24H2 with Docker Desktop, mirrored networking | latest stable | Ubuntu | NET005 WARN, PLG001 OK |

Hyper-V VMs with nested virtualization enabled are sufficient for all rows except arm64.

## 6. Decisions to ratify before Phase 1

1. Go, with the dependency allow-list in §1.
2. Stdlib CLI, no framework.
3. No self-elevation in v1 (§3.4).
4. Snapshot-first testing with Linux-enforced probe purity (§5.2, §5.3).
5. Signing provider (§4.2).
6. JSON schema `v1` frozen at M1 release; additive-only thereafter.

## 7. Sources checked for this document (2026-09-12)

- `golang.org/x/sys/windows` event log API: https://github.com/golang/sys/pull/95/files
- `go-winio/vhd`: https://pkg.go.dev/github.com/Microsoft/go-winio/vhd
- Windows arm64 hosted runners for public repos: https://github.blog/changelog/2025-04-14-windows-arm64-hosted-runners-now-available-in-public-preview/
- Nested virtualization on hosted Windows runners is unsupported: https://github.com/actions/runner-images/issues/10563 and https://github.com/actions/runner-images/issues/12933
