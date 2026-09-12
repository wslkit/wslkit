# ADR 0007 — Phase 0 spike results (S1–S4)

Status: recorded, 2026-09-12. Machine: Windows 10 Pro 10.0.19045, WSL 2.7.13.0 (Store
package, installed to `C:\Program Files\WSL`), **not elevated**. Program: `spikes/apis`.

## S1 — WMI from Go without PowerShell: YES

Late-bound COM via `go-ole` (`WbemScripting.SWbemLocator` → `ConnectServer` →
`ExecQuery`) works unelevated.

| Query | Time | Notes |
|---|---|---|
| `Win32_OptionalFeature` (4 names) | 611 ms | Acceptable when run concurrently with other collectors. `InstallState` 1 = enabled, 2 = disabled. |
| `Win32_ComputerSystem` | 39 ms | `HypervisorPresent`, `TotalPhysicalMemory` |
| `Win32_Service` (5 names) | 853 ms | **Too slow.** Use the Service Control Manager directly (`x/sys/windows/svc/mgr`). |

Service names on a Store/MSI install: `WSLService` (running, Auto), `LxssManager`
(stopped, Manual: inbox legacy), `vmcompute`, `HvHost`.

## S2 — Runtime and distro facts without `wsl.exe`: YES

- `HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss\{guid}` values present on 2.7.13:
  `State DistributionName Version BasePath Flags DefaultUid RunOOBE VhdFileName Flavor
  OsVersion Modern ShortcutPath TerminalProfilePath`. `Flavor="ubuntu"`, `OsVersion="26.04"`,
  `Modern=1` for the founding distro. A third-party alpine-based distro also reports
  `Flavor="alpine"`, `OsVersion="3.24.1"`, `Modern=1`.
- `HKLM\...\Lxss`: `KernelVersion` (stale inbox value 5.10.x; not the running kernel),
  `NatNetwork`, `NatGatewayIpAddress`; subkeys `MSI` (`InstallLocation`, `ProductCode`,
  `Version`), `Plugins` (empty here), `DiskMounts`.
- Runtime version: `GetFileVersionInfo` on `C:\Program Files\WSL\wslservice.exe` returns
  `2.7.13.0`, identical to `wsl --version`. 1 ms.
- Appx full name with version is readable from
  `HKCU\Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`
  and from `HKLM\...\Appx\AppxAllUserStore\Applications`.
- Inbox `C:\Windows\System32\wsl.exe` reports the OS build version (10.0.19041.x), which
  is how to tell inbox-only installs apart.
- CLSID `{a9b7a1b9-…}` present, `{e66b0f30-…}` absent on a working machine. The two
  CLSIDs are not both required; `HST005` must learn from source which one belongs to
  which runtime flavor before it can flag absence.
- `Tcpip6\Parameters\DisabledComponents` readable (not set here).

## S3 — Defender exclusions unelevated: NO (partially)

`MSFT_MpPreference` is queryable unelevated but returns the literal string
`N/A: Must be an administrator to view exclusions` for `ExclusionPath` and
`ExclusionProcess`. `DisableRealtimeMonitoring` and `MSFT_MpComputerStatus`
(`AMRunningMode`, `RealTimeProtectionEnabled`, `AMProductVersion`) are readable.
The registry key `HKLM\SOFTWARE\Microsoft\Windows Defender\Exclusions\Paths` is
access-denied.

Consequence: `DEF001` unelevated reports `UNKNOWN` with "real-time protection is on;
exclusions need `--elevated`". Elevated it can do the full check. PLAN §5 is corrected.

## S4 — Event log channels unelevated

| Channel | Unelevated |
|---|---|
| `Microsoft-Windows-Hyper-V-VmSwitch-Operational` | readable (4.3 M records here) |
| `System`, `Application` | readable; XPath filters for WER / Application Error and Kernel-Power 42/107 work |
| `Microsoft-Windows-Hyper-V-Compute-Admin` / `-Operational` | **access denied** |
| `Microsoft-Windows-Hyper-V-Worker-Admin` | **access denied** |
| `Microsoft-Windows-Host-Network-Service-*` | **access denied** |

Consequence: `EVT001` unelevated uses VmSwitch + System + Application. Compute and HNS
correlation requires `--elevated`.

## Other

- `%TEMP%\wsl-crashes` exists (empty). `C:\Windows\temp\wsl-install-log.txt` is
  access-denied unelevated.
- `.wslconfig` absent on this machine, so lint fixtures must be synthetic.

## Follow-up from the first CI runs (same day)

- **`Microsoft-Windows-Subsystem-Linux` is not a signal for legacy WSL.** A Windows 11
  25H2 arm64 runner with no WSL at all reports the feature as enabled, has the installer
  stub `wsl.exe` (OS build version) and no `LxssManager` service. Legacy in-box WSL is
  detected by the `LxssManager` service existing; "not installed" is stub present, no
  runtime, no service.
- **`Win32_OptionalFeature` can answer partially on a cold WMI repository.** A Server
  2025 runner omitted `VirtualMachinePlatform` on the first query and returned it on the
  next two. The collector retries once; probes treat "not returned" as UNKNOWN.
- Hosted runners: `windows-latest` (Server 2025) ships WSL 2.7.13 with no distros and
  Defender real-time protection off; `windows-11-arm` ships no WSL. Both are fixtures now.
- `Win32_QuickFixEngineering` is readable unelevated (0.96 s for 91 rows; 0.64 s
  filtered). The Hyper-V firewall classes `MSFT_NetFirewallHyperVVMCreator` and
  `MSFT_NetFirewallHyperVProfile` in `ROOT\standardcimv2` return `Invalid class` on
  Windows 10 22H2 in ~40 ms, matching WSL's own "no Hyper-V firewall support" branch
  (`spikes/qfe`).

## S5 — answered from source and release notes (same day, VM confirmation pending)

Ubuntu 26.04 removed cgroup v1 (legacy and hybrid) and ships systemd 259; WSL 2.5.1
(2025-03-12) "Remove cgroupv1 support", first stable 2.5.7 (2025-04-24). A 2.4.x runtime
mounts a hybrid v1 hierarchy that systemd 259 refuses, so the distro never boots and
`wslservice` reports `Wsl/Service/E_UNEXPECTED`. `compat.json` now carries
`min_runtime = 2.5.7` for ubuntu 26.04. Full write-up and the generalisation to any
cgroup-v2-only distro: `docs/research/2026-09-feature-research.md` §1. The earlier
citation of issue #13484 was wrong: that is a corrupted-VHD case with the same error string.

Still worth doing on a scratch VM: install 2.4.13 and 2.5.7 MSIs with the Ubuntu 26.04
`.wsl` and confirm fail/boot, then mark the row as verified.
