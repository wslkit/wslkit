# wsldoctor — Plan

> Status: **not started**. This document is the specification. It exists so a fresh
> Claude Code session (or any contributor) can pick up development with full context.

## 1. What it is

One native Windows CLI that answers **"why is my WSL broken or slow?"** and then fixes
what it finds.

```
wsldoctor check              # read-only, no admin, ranked diagnosis
wsldoctor fix <action>       # explicit, per-action remediation
```

Not a monitoring tool, not a GUI, not a distro manager. A one-shot diagnostic that ends
with *"here is your problem, here is the command that fixes it."*

## 2. Why it exists

### The founding bug

On 2026-09-10 a fresh `Ubuntu 26.04` install failed on every launch with:

```
Catastrophic failure
Error code: Wsl/Service/E_UNEXPECTED
```

Finding the cause took ~12 manual commands: `wsl --status`, `--list -v`, `--version`,
event-log queries against `Microsoft-Windows-Hyper-V-VmSwitch`, three registry reads under
`HKCU\...\Lxss`, a VHDX header inspection, a `--system` distro probe, and one wrong
hypothesis (a bad OOBE) that had to be tested and reverted.

The actual cause: **WSL runtime 2.4.13.0 (January 2025) cannot boot an Ubuntu 26.04
modern/tar-format distro.** `wsl --update` to 2.7.13 fixed it in one command.

**Nothing in the ecosystem checks runtime-version against distro-version compatibility.**
That single probe would have collapsed a 40-minute investigation into one line of output.
It is Probe `WSL001` below and it is the reason this project exists.

### Demand evidence

Open issue reaction counts on `microsoft/WSL`, sampled 2026-09-12:

| Reactions | Issue | Probe |
|---|---|---|
| 1418 | WSL 2 should automatically release disk space back to the host OS | `DSK001` |
| 452 | WSL 2 consumes massive amounts of RAM and doesn't return it | `MEM001` |
| 415 + 159 | Zone.Identifier files when downloading/copying from Windows | `ZON001` |
| 259 | WSL is non-responsive after waking from hibernate | `HIB001` |
| 247 | DNS issues in WSL2 | `NET002` |
| 205 | WSL don't start, don't open and don't answer | `WSL001`+ |
| 130 | VHD files must be uncompressed, unencrypted, not sparse | `DSK003` |
| 119 | Windows Defender is destroying WSL2 performance | `DEF001` |
| 111 | WSL2 distro failing to startup with code 4294967295 | `WSL001`+ |
| 93 | WSL2 corrupts ext4 filesystem | `DSK004` |

### Prior art (verified 2026-09-12 — recheck before launch)

Three GitHub repos are named `wsl-doctor`. **All three have 0 stars.** The most recent
(`Golopmoui3/wsl-doctor`, pushed 2026-09) only regenerates `resolv.conf`. The idea has been
claimed repeatedly and executed by nobody.

Adjacent tools that are *not* competitors but should be linked from output rather than
reimplemented:

- `sakai135/wsl-vpnkit` (2,957 stars) — VPN connectivity. **Do not rebuild.** Detect and recommend.
- `pxlrbt/move-wsl` (1,555 stars), `okibcn/wslcompact` (1,183 stars) — VHDX move/compact.
- `bostrot/wsl2-distro-manager` (3,995 stars), `owu/wsl-dashboard` (3,752 stars) — GUI managers.
- `dorssel/usbipd-win` (6,134 stars) — USB passthrough.

## 3. Design principles

1. **`check` never mutates.** No registry writes, no service restarts, not even
   `wsl --shutdown`. People paste `check` output into bug reports; it must be safe to run
   on a machine mid-crisis.
2. **No admin for `check`.** Every probe must degrade gracefully when not elevated and say
   so, rather than failing. See section 7.
3. **Fixes are explicit and per-action.** There is no `--fix-all`. Each fix names exactly
   what it will change and dry-runs by default.
4. **Rank by likely cause, not by severity.** Ten yellow warnings and one red root cause
   means the root cause prints first.
5. **Every finding ends in an action.** A finding with no suggested command is a bug.
6. **Link out, don't reimplement.** When a mature tool owns a problem, recommend it.
7. **No PowerShell dependency at runtime.** Consistent with `wsldisk` / `wsldrive`.
   Note that some probes are *easiest* via WMI/CIM — use the Win32 APIs directly.

## 4. Architecture

```
cmd/wsldoctor/         CLI entry, arg parsing, output rendering
internal/probe/        Probe interface + registry
internal/probe/wsl/    Runtime/distro probes    (WSL***)
internal/probe/host/   Windows host probes      (HST***, DEF***)
internal/probe/disk/   VHDX + filesystem probes (DSK***)
internal/probe/net/    Networking probes        (NET***)
internal/probe/perf/   Memory/CPU probes        (MEM***)
internal/fix/          Remediation actions
internal/winapi/       Registry, services, event log, WMI wrappers
internal/render/       Human output + --json
```

Every probe implements one interface:

```go
type Probe interface {
    ID() string                          // "WSL001"
    Title() string                       // "Runtime/distro version compatibility"
    RequiresElevation() bool             // false for all check-time probes
    Run(ctx context.Context, env *Env) Result
}

type Result struct {
    Status     Status   // OK | Warn | Fail | Skipped | Unknown
    Summary    string   // one line, the finding
    Detail     string   // evidence: values read, versions compared
    Confidence float64  // 0..1, drives ranking
    FixID      string   // "" if no automated fix
    FixHint    string   // command the user should run
    Refs       []string // issue/doc URLs
}
```

`Env` is gathered **once** and shared: WSL version, distro inventory from the registry,
host OS build, elevation state, service states. Probes must not each shell out to
`wsl.exe` — that is slow and can wake the VM.

### Language

**Recommendation: Go.** Matches `skrog`, ships a single static binary with no runtime,
and `golang.org/x/sys/windows` covers registry, services and event log adequately. This is
probe-and-report logic, not hot-path I/O, so C++ (as in `wsldisk`/`wsldrive`) buys nothing
here. **Revisit if** the ext4 probe (`DSK004`) ends up wanting to link `wsldisk` internals.

## 5. Probe catalog

Ordered by implementation priority. `M1` probes are the minimum viable release.

### Runtime and distro

| ID | Milestone | Check | Source of truth |
|---|---|---|---|
| `WSL001` | **M1** | **Runtime version vs distro OS version.** The founding bug. Build a compat matrix: modern/tar-format distros need a minimum WSL build. Flag "runtime older than distro" loudly. | `wsl --version`; `Lxss\{guid}\OsVersion`, `Flavor`, `Modern`, `Version` |
| `WSL002` | **M1** | WSL installed at all, and which flavor (Store `MicrosoftCorporationII.WindowsSubsystemForLinux` vs inbox). | Appx package, `wsl --version` |
| `WSL003` | **M1** | Update available. Compare installed against latest release. | `wsl --update --status`, GitHub releases API (cache, allow `--offline`) |
| `WSL004` | **M1** | Distro inventory + state: `State`, `Version` (1 vs 2), `Flags`, `BasePath`, `DefaultUid`, `RunOOBE`, `Modern`. Flag `RunOOBE=1` + `DefaultUid=0` as "first-run setup never completed". | `HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss` |
| `WSL005` | M2 | `.wslconfig` lint — unknown keys (silently ignored, a top support burden), wrong section headers, version-gated keys used on too-old a runtime, malformed values. | `%USERPROFILE%\.wslconfig` + version matrix |
| `WSL006` | M2 | `/etc/wsl.conf` lint per distro. Requires booting the distro — skip cleanly if it won't start, and *say* it was skipped. | distro fs |
| `WSL007` | M3 | systemd enabled/healthy; `automount.cgroups` version sanity (new in 2.7). | `/etc/wsl.conf`, `systemctl is-system-running` |

### Windows host

| ID | Milestone | Check | Notes |
|---|---|---|---|
| `HST001` | **M1** | Optional features: `Microsoft-Windows-Subsystem-Linux`, `VirtualMachinePlatform`, `Hyper-V`. | **Use `Win32_OptionalFeature` via WMI — `Get-WindowsOptionalFeature`/DISM requires elevation and will fail the no-admin rule.** Verified during the founding investigation. |
| `HST002` | **M1** | Services: `LxssManager`, `vmcompute`, `HvHost` — state and start type. | Service Control Manager |
| `HST003` | **M1** | Virtualization enabled in firmware; nested-virt / competing hypervisor (VMware, VirtualBox) detection. | `Win32_ComputerSystem`, CPUID |
| `HST004` | M2 | Windows build vs WSL feature requirements; pending reboot after a feature change. | |
| `DEF001` | **M1** | **Windows Defender exclusions.** Check whether `ext4.vhdx`, `%LOCALAPPDATA%\wsl`, `vmmem`/`vmwp` are excluded. **Reading exclusions is possible unelevated via `MpPreference`; *setting* them is admin-only — this is the one fix that elevates.** | 119 reactions |
| `HIB001` | M2 | Detect a resume-from-hibernate state where the VM is wedged; correlate last resume time against VM health. | 259 reactions |

### Disk

| ID | Milestone | Check | Notes |
|---|---|---|---|
| `DSK001` | **M1** | VHDX actual size vs used space inside the distro. Report reclaimable bytes. | 1418 reactions. **Recommend `wsldisk`**, do not reimplement compaction. |
| `DSK002` | **M1** | VHDX exists at `BasePath`, header magic is `vhdxfile`, file is non-zero and readable. | Cheap corruption smoke test |
| `DSK003` | M2 | VHDX must not be sparse, compressed, or encrypted — WSL fails on all three. | 130 reactions |
| `DSK004` | M3 | ext4 integrity. **Technique: mount the target vhdx from the WSL *system* distro as a rescue environment** (see section 9) and run a read-only `fsck -n`. | 93 reactions |
| `DSK005` | M2 | Free space on the host volume holding the vhdx; warn before WSL wedges on a full disk. | |
| `ZON001` | M2 | Count `Zone.Identifier` alternate data streams under the distro's Windows-visible paths. | 574 reactions combined |

### Networking and memory

| ID | Milestone | Check | Notes |
|---|---|---|---|
| `NET001` | M2 | Networking mode (NAT vs mirrored); mirrored-mode + VPN known-breakage. | 642 and 56 reactions |
| `NET002` | M2 | DNS resolution from inside the distro; `generateResolvConf` vs a hand-edited `resolv.conf`; **active VPN adapter detection**. | 247 reactions. **Recommend `wsl-vpnkit`, do not rebuild it.** |
| `NET003` | M3 | Throughput smoke test to flag the "very slow network" class. | 248 reactions |
| `MEM001` | **M1** | vmmem/vmmemWSL working set vs configured `memory=` cap; flag unbounded default on low-RAM hosts. | 452 reactions |
| `MEM002` | M3 | Suggest idle reclaim settings; report cache pressure inside the distro. | |

### Correlation

| ID | Milestone | Check |
|---|---|---|
| `EVT001` | M2 | Pull the last N minutes of `Microsoft-Windows-Hyper-V-VmSwitch`, `Hyper-V-Compute` and Lxss events and correlate against a failed launch. **During the founding bug these events proved the VM was booting fine, which is what isolated the fault to the distro rather than to virtualization.** That inference should be automatic. |

## 6. Fix catalog

| Fix ID | Command | Elevates | What it changes |
|---|---|---|---|
| `update` | `wsldoctor fix update` | no | Runs `wsl --update`. Resolves `WSL001`/`WSL003`. |
| `defender` | `wsldoctor fix defender` | **yes** | Adds Defender exclusions for the vhdx paths and WSL processes. Must print the exact exclusion list and require confirmation. |
| `zone` | `wsldoctor fix zone` | no | Deletes `Zone.Identifier` ADS. Needs `--path` scoping and a dry-run count first. |
| `oobe` | `wsldoctor fix oobe` | no | Resets `RunOOBE` so first-run setup re-runs. **Reverted correctly during the founding investigation — the value must be restored if the fix does not help.** |
| `shutdown` | `wsldoctor fix shutdown` | no | `wsl --shutdown`. Explicit because it kills running work. |
| `wslconfig` | `wsldoctor fix wslconfig` | no | Comments out unknown/unsupported keys, with a backup. |

Every fix: dry-run by default, `--apply` to execute, always print a rollback instruction.

## 7. Elevation model

`check` must run fully unelevated. Where a probe cannot get its data without admin:

1. Try the unelevated path first (WMI over DISM, `MpPreference` read over policy read).
2. If genuinely unavailable, return `Status: Unknown` with
   `"requires --elevated to check"` — **never** fail the run, and never silently skip.
3. `wsldoctor check --elevated` re-runs only those probes.

This was learned the hard way: `Get-WindowsOptionalFeature` returned
`"The requested operation requires elevation"` while `Win32_OptionalFeature` returned the
same facts unelevated.

## 8. Output

Human-readable by default, ranked, root cause first:

```
wsldoctor 0.1.0   WSL 2.4.13.0   Windows 10.0.19045   1 distro

FAIL  WSL001  Runtime is too old for this distro
      Ubuntu is 26.04 (modern/tar format); WSL runtime is 2.4.13.0 (2025-01).
      Modern-format distros of this vintage need runtime >= 2.5.x.
      This will present as: Wsl/Service/E_UNEXPECTED on every launch.
      -> wsldoctor fix update

WARN  DEF001  Windows Defender is scanning your WSL disk
      No exclusion for <basepath>\ext4.vhdx  (1.4 GB, scanned on every write)
      -> wsldoctor fix defender          (requires admin)

OK    HST001  VirtualMachinePlatform, WSL feature, Hyper-V all enabled
OK    HST002  LxssManager, vmcompute, HvHost healthy
```

Also required:

- `--json` — stable schema for issue reports and CI.
- `--report` — a single pasteable markdown block, **with usernames and paths redacted**.
  People will paste this into public GitHub issues; scrub user profile paths by default.

## 9. Techniques worth knowing

**The system distro as a rescue environment.** `wsl -d <distro> --system -- <cmd>` boots
WSL's own minimal system distro. During the founding investigation this worked while
Ubuntu itself would not boot — which is what proved the runtime and VM were healthy and
isolated the fault to the distro image. Use it for:

- Inspecting a vhdx that will not mount normally (`DSK002`, `DSK004`).
- Distinguishing "WSL is broken" from "this distro is broken" — a critical early branch
  in the diagnosis tree.

**VHDX growth as a liveness signal.** During the failure, `ext4.vhdx` grew ~32 MB across
launch attempts. That proved the disk was being mounted and written, ruling out
corruption, before any deeper check ran. Cheap and diagnostic.

## 10. Milestones

- **M0 — skeleton.** CLI, probe interface, `Env` gathering, renderer, `--json`. No probes.
- **M1 — the release that justifies the project.** `WSL001`–`WSL004`, `HST001`–`HST003`,
  `DEF001`, `DSK001`, `DSK002`, `MEM001`, plus `fix update` and `fix defender`.
  `WSL001` alone is the launch story.
- **M2 — breadth.** `.wslconfig` lint, networking, Zone.Identifier, event correlation,
  hibernate, remaining disk probes.
- **M3 — depth.** ext4 integrity, throughput tests, systemd, memory tuning advice.

Ship M1 before starting M2. A tool that nails one painful diagnosis beats one that
half-covers twenty.

## 11. Open decisions

- **Language** — Go recommended (section 4). Not yet ratified.
- **Compat matrix source** — hand-maintained table vs scraped from WSL release notes.
  Hand-maintained is fine to start; it changes a few times a year.
- **Distribution** — Scoop (org already has `scoop-skrog`), winget, GitHub Releases.
- **Should `check` auto-detect and offer fixes interactively?** Leaning no: keeps `check`
  provably read-only.
- **Relationship to `skrog doctor`** — extract a shared `wslkit-probe` module, or keep
  them independent? Defer until M1 ships.

## 12. Launch checklist

- [ ] Flip repo to public
- [ ] Submit to `sirredbeard/awesome-wsl` (6,554 stars — the canonical discovery path)
- [ ] Cross-link from `wsldisk` / `wsldrive` / `skrog` READMEs
- [ ] Answer the long-tail "WSL won't start" issues on `microsoft/WSL` with real diagnosis
- [ ] Re-verify prior art; three `wsl-doctor` repos exist, all currently 0 stars

## 13. References

- Founding bug: WSL 2.4.13.0 + Ubuntu 26.04 produced `Wsl/Service/E_UNEXPECTED`, fixed by `wsl --update`
- `microsoft/WSL` issues: https://github.com/microsoft/WSL/issues
- WSL release notes: https://learn.microsoft.com/en-us/windows/wsl/release-notes
- Registry: `HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss`
- WSL containers preview (ecosystem context): https://devblogs.microsoft.com/commandline/wsl-container-is-now-available-for-public-preview/
