# wsldoctor — Development phases and feature research

> **2026-09-12: wsldoctor is now `wslkit doctor`** (ADR 0008). Command names below read
> with the `wslkit doctor` prefix; probe and fix IDs are unchanged. Research for the
> other planned subcommands (`sock`, `limit`, `top`, `guard`, `disk`, `proxy`) lives in
> `docs/research/2026-09-subcommands.md`.

> Companion to [PLAN.md](PLAN.md) and [ARCHITECTURE.md](ARCHITECTURE.md). PLAN.md is the specification (what the probes are and
> why). This document is the *how and when*: engineering phases, spikes that must land
> before code, and research into features the plan does not yet cover.
> Written 2026-09-12 against WSL 2.7.14 stable / 2.9.11 pre-release. Re-verify before use.

## 0. What changed since PLAN.md was drafted

Facts that alter priorities or add scope. Each maps to a section below.

| Change | Consequence for wsldoctor |
|---|---|
| WSL is open source since Build 2025 (`microsoft/WSL`: `wslservice.exe`, `wsl.exe`, plan9 server, Linux daemons). Only `lxcore.sys`, `p9rdr.sys`, `p9np.dll` remain closed. | Error strings, registry keys, config parser and the modern-distro loader can be read from source instead of reverse-engineered. Compat matrix and error dictionary become *derivable*. See §3.1, §3.2. |
| Stable line is 2.7.x (2.7.14 on 2026-09-11). Pre-release 2.9.x adds **WSL containers** (`wslc`), kernel 6.18. | Runtime version parsing must handle two concurrent lines. `wslc` is a new failure surface but pre-release; watch, don't probe yet. See §3.9. |
| 2.7.11–2.7.13 fixed: VHD ownership after cross-volume `--move`, plugin-folder mount permissions (Docker Desktop, VS Code Remote), an **MDE plugin error that prevented WSL from starting at all**. | Two new probe families: VHD ACL/ownership, and **plugin health**. See §3.4, §3.5. |
| Most-reacted issues created after mid-2025 are dominated by networking: KB5068861 breaks mirrored+VPN (51), mirrored `Failed to configure network` (24, 9), systemd user session fails to start (31), crash on wake from sleep (7). | NET probes move up. A `systemd` user-session probe is new. See §3.6, §3.7. |
| The founding bug has a public issue: `microsoft/WSL#13484` "Error code: Wsl/Service/E_UNEXPECTED". | Cite it in `WSL001` output. |
| Microsoft's own tooling: `diagnostics/collect-wsl-logs.ps1`, `collect-networking-logs.ps1`, the WinUI **WSL Settings** app, `wsl --debug-shell`, `debugConsole=true`, crash dumps in `%TEMP%\wsl-crashes`. | wsldoctor sits *before* these: fast triage, no ETW, no admin. `--report` should tell people when to escalate to `collect-wsl-logs.ps1`. The script's data-source list is also the best available checklist for `Env` (Phase 1). |
| Modern/tar (`.wsl`) distros are installable from WSL 2.4.4; Canonical recommends ≥ 2.4.8. The founding failure was on **2.4.13**. | "Supports modern format" is not the compat criterion. The matrix must be **per distro release**, not per format. Bisecting the true minimum for Ubuntu 26.04 is a Phase 0 research task. |
| `Get-MpPreference` / `MSFT_MpPreference` reads are reported to **require local admin** on current builds, and the Defender exclusions registry hive is no longer readable on Windows 11. | Contradicts PLAN §5 `DEF001` ("reading exclusions is possible unelevated"). Must be verified in a spike before M1 scope is fixed. See Phase 0, S3. |

## 1. Development phases

Phases are sequential gates; the milestone tags (M0–M3) from PLAN.md map onto them.

### Phase 0 — Spikes (1–2 weeks, throwaway code)

> **Status 2026-09-12:** S1–S4 answered on a Windows 10 22H2 machine, see
> [ADR 0007](docs/decisions/0007-spike-results.md). Headlines: WMI works unelevated but
> service queries are slow (use SCM directly); Defender exclusions are admin-only to read;
> VmSwitch/System/Application channels are readable unelevated, Hyper-V Compute and HNS
> are not. S5 answered from source and release notes the same day (cgroup v1 removal in
> WSL 2.5.1 vs cgroup-v2-only Ubuntu 26.04); VM confirmation pending. Deeper feature
> research from the WSL source is in `docs/research/2026-09-feature-research.md`.

Nothing in Phase 1 should start until these five questions have a measured answer.
Each spike is a small Go program in `spikes/` that prints what it found; they are deleted
once the answer is recorded in `docs/decisions/`.

| # | Question | Why it gates design | Exit criterion |
|---|---|---|---|
| S1 | Can `Win32_OptionalFeature`, `Win32_ComputerSystem` (`HypervisorPresent`), `Win32_Service` be queried from Go over raw COM/WMI (`go-ole` + `IWbemServices`) with **no PowerShell**, unelevated, in < 300 ms? | Principle 7 (no PowerShell) and 2 (no admin) both depend on it. | Working query helper; timings on Win10 19045 and Win11 24H2. |
| S2 | Can `Lxss` registry (HKCU + HKLM), Appx package version (`MicrosoftCorporationII.WindowsSubsystemForLinux`) and `wsl.exe` file version be read without invoking `wsl.exe`? | `Env` must not wake the VM. | Version string identical to `wsl --version` on 3 machines. |
| S3 | Can Defender exclusions be read unelevated by *any* route: `root/microsoft/windows/defender` WMI, `MpCmdRun.exe`, registry, MDE plugin state? | Decides whether `DEF001` is M1 or M1-`--elevated`-only. | Yes/no per route, per OS build; if all no, `DEF001` degrades to `Unknown` unelevated. |
| S4 | Which classic event log channels carry WSL-relevant events readable via `wevtapi` unelevated: `Microsoft-Windows-Hyper-V-VmSwitch`, `Hyper-V-Compute-Admin/Operational`, `Host-Network-Service`, `Application` (WER crashes for `wslservice.exe`, `wsl.exe`)? The WSL TraceLogging providers (`Microsoft.Windows.Lxss.Manager`, `Microsoft.Windows.Subsystem.Lxss`, `Microsoft.Windows.Plan9.Server`) need a live ETW session — is that possible without admin (Performance Log Users)? | Decides `EVT001` design: passive log read vs. active trace-while-launch. | Table of channel → readable unelevated (y/n) → useful events. |
| S5 | Bisect the founding bug: which exact runtime version first boots Ubuntu 26.04 (`.wsl`)? Install 2.4.13, 2.5.x, 2.6.x MSIs from GitHub releases on a scratch VM. Read `wslservice` source for the modern-distro loader to name the feature that gated it. | The compat matrix needs a *real* first row, with a cause, not "update fixed it". | Row in `data/compat.json` with `min_runtime`, `symptom`, `cause`, `ref`. |

### Phase 1 — Skeleton and test harness (M0)

> **Status 2026-09-12:** done, and it went further than "zero probes": WSL001–WSL004,
> HST001–HST003, DEF001 (degraded unelevated), PLG001, DSK001–DSK003, DSK005, MEM001,
> EVT001 are implemented, plus `fix update`, `fix shutdown`, `undo`, the snapshot corpus
> (two fixtures, founding bug included) and the CI workflows. Not yet done from this
> list: code signing, Scoop/winget manifests.

Deliverable: `wslkit doctor check` runs, gathers `Env`, renders zero probes, emits `--json`.

- **`Env` gathering**, one pass, no `wsl.exe` invocation unless `--allow-vm-wake`. Use
  `collect-wsl-logs.ps1` as the checklist of sources: HKCU/HKLM `Lxss`, `P9NP` and
  `WinSock2` service keys, WSL COM CLSIDs `{e66b0f30-…}` / `{a9b7a1b9-…}`, `Windows NT\CurrentVersion`,
  `Tcpip6\Parameters`, services `wslservice`/`LxssManager`/`vmcompute`/`HvHost`, Appx package,
  optional features, `.wslconfig`, `%TEMP%\wsl-install-logs.txt`, `%TEMP%\wsl-crashes`.
- **Snapshot mode.** `Env` serialises to JSON. `wslkit doctor check --from-snapshot env.json`
  runs every probe against a saved environment. Probes are pure functions of `Env`.
  This is the entire testing strategy: every bug report that includes `--json` output
  becomes a regression fixture in `testdata/snapshots/`. Build it in Phase 1, not later.
- **Renderer** with ranking (confidence × status), `--json` (versioned schema `v1`),
  `--report` (markdown, redacts `%USERPROFILE%`, SIDs, hostnames, machine GUIDs).
- **Probe registry** with milestone tags so `check --only M1` works during development.
- **CI**: GitHub Actions `windows-latest` runs unit tests + snapshot corpus; a nightly
  job runs live `check` on the runner and stores the snapshot as an artifact so drift in
  Windows images is visible.
- **Release plumbing**: goreleaser, signed binaries (Authenticode via Azure Trusted
  Signing or SignPath OSS), Scoop manifest in the existing bucket, winget manifest.

### Phase 2 — M1: the launch release

Exactly the PLAN §10 M1 list, with these amendments from research:

- `WSL001` uses the per-release compat matrix from S5, cites `microsoft/WSL#13484`.
- `WSL003` compares against the **stable** channel only unless `--pre-release`; the
  GitHub releases API returns both lines interleaved.
- `DEF001` ships in whatever form S3 allows. If unelevated reads are impossible on Win11,
  the probe prints `UNKNOWN  requires --elevated` and M1 still ships.
- New M1 probe **`HST005` COM registration**: both WSL CLSIDs present, `wslservice`
  registered. Detects `0x80040154 REGDB_E_CLASSNOTREG` after a Windows update, which the
  official troubleshooting page lists and which has a one-line fix (`wsl --update` or
  Store repair).
- New M1 probe **`PLG001` plugin health**: enumerate `HKLM\…\Lxss\Plugins`, check each
  plugin DLL exists and is loadable, flag the MDE plugin on runtimes < 2.7.13.
  Justification: a plugin fault was a *total* startup failure in 2.7.12.
- `fix update`, `fix defender`, plus `fix shutdown` (needed by almost every other fix's
  rollback instructions).

Launch gate: `check` on a healthy machine prints all-OK in under 2 s; `check` on the
founding-bug snapshot prints `WSL001 FAIL` first.

### Phase 3 — M2: breadth

- `.wslconfig` lint (`WSL005`) driven by a **key table with version gates** generated
  from the docs and source (§3.3). Unknown keys, Win10-only machines using Win11-only
  keys, `[experimental]` keys that have graduated to `[wsl2]`, deprecated `bridged`.
- Networking family (`NET001`–`NET002`, new `NET004`–`NET006`, §3.6).
- `EVT001` in the form S4 permits.
- `HIB001` extended to sleep/resume (`PWR001`, §3.7).
- `ZON001`, `DSK003`, `DSK005`, `DSK006` (VHD ownership, §3.4).
- `wslkit doctor explain <error>` (§3.2) and `wsldoctor preflight` (§3.11).
- `fix zone`, `fix oobe`, `fix wslconfig`.

### Phase 4 — M3: depth

- `DSK004` ext4 read-only `fsck` via the system distro (`wsl --system`).
- `SYS001` systemd health incl. the user-session and `XDG_RUNTIME_DIR` failures (§3.7).
- `NET003` throughput smoke test (opt-in, `--slow`).
- `MEM002` reclaim advice; `GPU001`/WSLg health (§3.8).
- Interactive `wsldoctor triage` (§3.10) only if M1/M2 feedback asks for it.

### Phase 5 — Launch and sustainment

PLAN §12 checklist plus:

- **Compat matrix and key table are data, not code**, updated by a scheduled CI job that
  diffs the GitHub releases API and the `wsl-config.md` doc and opens a PR. Humans
  approve; the binary ships the table embedded and also fetches a newer one with
  `--online` (cache, TTL 24 h, `--offline` honoured).
- **Snapshot donation.** `--report` ends with "attach `wsldoctor-env.json` to help".
  A `CONTRIBUTING.md` section explains that a snapshot + expected finding = a test.
- Triage template for `microsoft/WSL` `failure-to-launch` issues asking for
  `wslkit doctor check --report`. Adoption path, and free fixtures.

## 2. Cross-cutting engineering decisions to ratify

| Decision | Recommendation | Reason |
|---|---|---|
| Language | **Go**, ratify now. | S1/S2 spikes are cheap in Go; `golang.org/x/sys/windows` covers registry, SCM, wevtapi. COM/WMI via `go-ole` is the only risk and S1 retires it. |
| WMI vs. direct API | Direct Win32 where it exists (services, registry, event log, file attributes, VHDX header). WMI only for optional features, hypervisor presence, Defender. | WMI adds 100–300 ms per query and a COM apartment; keep it to three probes. |
| Waking the VM | Never by default. `wsl.exe` calls are behind `--allow-vm-wake`; the renderer marks probes skipped for that reason. Per-probe context deadline, default 5 s. | A wedged `vmmem` means `wsl.exe` hangs. `check` must finish regardless. |
| Testing | Snapshot corpus is the primary test; live runs are smoke only. | You cannot CI a broken WSL. You can CI a JSON file describing one. |
| Fix safety | Every `fix` writes `%LOCALAPPDATA%\wsldoctor\undo\<timestamp>.json` with prior values, and `wslkit doctor undo <id>` replays it. | PLAN requires a printed rollback; a stored one is stronger and costs little. |
| Redaction | `--report` redacts by default; `--no-redact` opt-out. Redact before rendering, not after, so JSON and markdown agree. | Output is destined for public issues. |

## 3. Feature research

Each item: evidence, technique, elevation, proposed ID and phase.

### 3.1 Compat matrix from source, not folklore

`wslservice` is open. The loader for modern distros (parsing of the tar's
`/etc/wsl-distribution.conf`, the `Flavor`, `OsVersion`, `Modern` registry values) tells
you exactly which runtime introduced each capability. Proposed `data/compat.json` row:

```json
{ "flavor": "ubuntu", "os_version": "26.04", "format": "modern",
  "min_runtime": "2.5.7", "symptom": "Wsl/Service/E_UNEXPECTED on every launch",
  "cause": "cgroup v2-only distro on a runtime that still mounts cgroup v1 (removed in WSL 2.5.1)",
  "refs": ["https://github.com/microsoft/WSL/releases/tag/2.5.1"] }
```

Also record *host* requirements: mirrored networking and Hyper-V firewall need
Windows 11 22H2+, several `[wsl2]` keys are Windows 11 only, `wsl.conf [boot]` needs
Windows 11 / Server 2022. That becomes `HST004`.

### 3.2 `wslkit doctor explain <error>` — the error-code dictionary

WSL error strings are call paths: `Wsl/Service/RegisterDistro/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED`,
`Wsl/Service/E_UNEXPECTED`, `Wsl/WSL_E_DEFAULT_DISTRO_NOT_FOUND`, `Wsl/InstallDistro/E_UNEXPECTED`.
Plus HRESULTs the docs list: `0x80370102` (virtualization off), `0x80040154` (COM class
not registered), `0x80040306`, `0x8007019e` (feature not enabled), `0x80070003`,
`0x8000FFFF`, `0x1bc`, exit code `4294967295`.

Technique: parse the path, map each segment to the probes most likely to explain it, run
only those, and print the known causes from the dictionary. Segment names come straight
from the `wslservice` source, so the dictionary can be generated and kept honest by a
test that greps the vendored source for every `WSL_E_*` and `Wsl/…` string.

`microsoft/WSL#12132` ("better error messages and known exit codes") shows demand from
Microsoft's side; wsldoctor can ship the dictionary they have not. Phase 3.

### 3.3 `.wslconfig` and `wsl.conf` key tables with gates

From the current `wsl-config.md`: `[wsl2]` has 20 keys, `[experimental]` 8, and two
footnote classes (Windows 11 only; Windows 11 22H2+). `networkingMode` accepts
`none|nat|bridged(deprecated)|mirrored|virtioproxy`; NAT silently falls back to
`virtioproxy` since 2.3.25. `sparseVhd` makes new VHDs sparse on purpose, so `DSK003`
must distinguish "sparse because WSL made it so" (fine) from "sparse/compressed/encrypted
by the user or by NTFS compression on the folder" (fails).

Lint rules worth shipping: unknown key (silently ignored by WSL, top support burden);
key in wrong section (`[experimental]` vs `[wsl2]` after graduation); Win11-only key on
Win10; `memory=` larger than physical RAM; `kernel=` path missing; `swapFile` on a
volume with < 2× swap free; `localhostForwarding` set with `mirrored` (ignored).
`wsl.conf` lint needs the distro fs: read via `\\wsl.localhost\<distro>\etc\wsl.conf`
only if the distro is *already running* (never wake it), else skip and say so. Phase 3.

### 3.4 VHD ownership and ACLs (`DSK006`)

2.7.11 and 2.7.12 both fixed VHDs left owned by `BUILTIN\Administrators` after
`wsl --move` / `--import` across volumes; symptom is "access denied" on launch.
`collect-wsl-logs.ps1` gathers `Get-Acl` on the VHD for exactly this reason. Probe:
owner and DACL of `ext4.vhdx` vs. current user SID; no elevation to read. Fix: set owner
via `SetNamedSecurityInfo`, elevated only if the current user is not the owner. Phase 3.

### 3.5 Plugin health (`PLG001`)

WSL loads host plugins (Docker Desktop, Microsoft Defender for Endpoint, VS Code) from
`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss\Plugins`. 2.7.13 exists solely
because an MDE plugin error prevented startup. Probe: enumerate plugins, verify DLL
exists and matches the WSL API version, flag known-bad plugin/runtime pairs. A plugin
that fails is indistinguishable to the user from "WSL is broken", which is precisely the
branch wsldoctor is for. Phase 2 (cheap; registry only).

### 3.6 Networking family, refreshed

| ID | Check | Evidence |
|---|---|---|
| `NET004` | Mirrored mode requested but host cannot honour it: Windows < 22H2, Hyper-V firewall unsupported (`wsl: Hyper-V firewall is not supported … falling back to NAT`), KB5068861 present with a VPN adapter. | `#10495`, `#13724` (51), `#13454` (24), `#13587` |
| `NET005` | Docker Desktop or Podman installed with mirrored mode: port-proxy conflicts, loopback unreachable; suggest `ignoredPorts` / `hostAddressLoopback`. | `#10926`, `#13868`, `#41204` |
| `NET006` | HNS state and phantom adapters: count `vEthernet (WSL*)` adapters, orphaned HNS networks, `Tcpip6` `DisabledComponents` set (IPv6 off breaks NAT), ICS service disabled. | troubleshooting doc sections on IPv6, ICS, phantom adapters |
| `NET002` | Add: `dnsTunneling` on but `bestEffortDnsParsing` off with a VPN that rewrites DNS; `.local` resolution; DNS suffix appending. | docs, `#13415` |

Windows-side data comes from `GetAdaptersAddresses`, HNS registry state, and Hyper-V
firewall settings via their APIs; keep `hnsdiag`/`vfpctrl` out and link to
`collect-networking-logs.ps1` for deep dives.

### 3.7 Power and systemd (`PWR001`, `SYS001`)

- `PWR001`: correlate `Kernel-Power` event 107 (resume) / 42 (sleep) times with a
  subsequent `wslservice` WER crash or `vmcompute` error. Recommend `wsl --shutdown`
  before sleep via a scheduled task, and explain the `vmIdleTimeout` trade-off.
  `#8696` (256), `#14193`.
- `SYS001`: `systemd=true` but `systemctl is-system-running` is neither running nor
  degraded; user session fails (`#13826`, 31) or `XDG_RUNTIME_DIR` vanishes after
  3 minutes (`#13562`). Check `protectBinfmt`, `loginctl enable-linger`, cgroup version.
  Requires a running distro; skip cleanly otherwise. Phase 4.

### 3.8 WSLg and GPU (`GPU001`)

`guiApplications` on but `/mnt/wslg` logs (`weston.log`, `pulseaudio.log`) show
failures; GPU paravirt disabled in `wsl.conf [gpu]`; `#8896` (GUI breaks after enabling
systemd). Read logs via `\\wsl.localhost\<distro>\mnt\wslg` only when running. Phase 4.

### 3.9 WSL containers (`wslc`) — watch list

2.9.x pre-release adds `wslc` (Docker-compatible CLI, virtiofs shares, healthchecks,
network connect/disconnect). Early issues: `E_INVALIDARG` on all commands (`#40944`),
proxy and registry-mirror gaps. Do **not** add probes until it reaches the stable line.
Do make the runtime-version parser tolerate the 2.9 line today so `WSL003` never
suggests "downgrading" a pre-release user to 2.7. Reassess at the first 2.8/2.9 stable.

### 3.10 Interactive triage (`wsldoctor triage`)

The official troubleshooting guide is a four-layer decision tree (binary → distro →
WSL stack → hardware) plus "does it reproduce in a Hyper-V VM". A guided mode could ask
the three questions `check` cannot infer (does another distro work? did it work before a
specific update? is a VPN connected right now?) and then run the matching probe subset.
Keeps `check` non-interactive and provably read-only. Phase 4, only if asked for.

### 3.11 Install pre-flight (`wsldoctor preflight <file.wsl|distro>`)

Given a `.wsl` file or a catalog name, check the runtime against the compat matrix
**before** `wsl --install --from-file`. Reads the tar's `/etc/wsl-distribution.conf`
and `/etc/os-release` without extracting. Would have prevented the founding bug outright
rather than diagnosing it afterwards. Phase 3; small.

### 3.12 Ideas considered and rejected

- **Compaction, moving VHDs, USB, VPN routing**: owned by `wsldisk`, `move-wsl`,
  `usbipd-win`, `wsl-vpnkit`. Recommend, never implement (PLAN principle 6).
- **ETW live tracing**: that is `collect-wsl-logs.ps1`; needs admin and WPA to read.
  wsldoctor links to it as the escalation path.
- **GUI**: WSL Settings and the third-party managers cover it.
- **Telemetry**: none. Snapshot donation is opt-in and manual.

## 4. Open research questions

> Tracked as GitHub issues since 2026-09-12: #1 (S5 VM confirmation), #10 (Defender
> elevated path), #16 (event channels and the passive "running" signal), #17 (COM
> CLSIDs). The remaining feature work from `docs/research/2026-09-feature-research.md`
> is issues #2–#9, #11–#15, #18; release plumbing is #19–#20.

1. S3 — is there *any* unelevated read of Defender exclusions on Windows 11 24H2+?
2. S4 — can a non-admin start a TraceLogging session for `Microsoft.Windows.Subsystem.Lxss`
   to catch `UserVisibleError`? If not, is parsing `wsl.exe` stderr (`--allow-vm-wake`)
   good enough?
3. S5 — the exact minimum runtime for Ubuntu 26.04 and the underlying cause.
4. How `Modern`, `Flavor`, `OsVersion` are populated for distros imported by third-party
   tools (Docker Desktop's `docker-desktop`, Rancher, Podman machine). They will trip a
   naive `WSL001`.
5. Whether `wsl --version` output and the Appx version can disagree (Store update
   pending vs. MSI install), and which one `wslservice` actually runs.
6. Whether `Win32_OptionalFeature` is reliable on Windows Server SKUs and on Windows 10
   LTSC where the WSL feature is inbox but the Store package is absent.

## 5. Sources consulted (2026-09-12)

- WSL releases API: https://api.github.com/repos/microsoft/WSL/releases
- Open-source announcement and scope: https://learn.microsoft.com/en-us/windows/wsl/opensource
- Config reference (key tables, footnotes): https://learn.microsoft.com/en-us/windows/wsl/wsl-config
- Troubleshooting (error codes, network sections): https://learn.microsoft.com/en-us/windows/wsl/troubleshooting
- Troubleshooting guide (layer model): https://learn.microsoft.com/en-us/windows/wsl/troubleshooting-guide
- Debugging doc (ETW providers, dump paths): https://github.com/microsoft/WSL/blob/master/doc/docs/debugging.md
- Log collector (Env checklist): https://github.com/microsoft/WSL/blob/master/diagnostics/collect-wsl-logs.ps1
- Modern distro format: https://learn.microsoft.com/en-us/windows/wsl/build-custom-distro and https://ubuntu.com/blog/ubuntu-wsl-new-format-available
- Founding bug public issue: https://github.com/microsoft/WSL/issues/13484
- Better error codes request: https://github.com/microsoft/WSL/issues/12132
- Mirrored / Hyper-V firewall fallback: https://github.com/microsoft/WSL/issues/10495
- Defender exclusion readability: https://www.huntress.com/blog/you-can-run-but-you-cant-hide-defender-exclusions
- WSL containers preview: https://devblogs.microsoft.com/commandline/wsl-container-is-now-available-for-public-preview/
- Issue reaction counts: GitHub search API, `repo:microsoft/WSL is:issue is:open`, sorted by +1
