# Feature research from primary sources (2026-09-12)

Grounding for the M1 remainder and M2 features. Sources: the `microsoft/WSL` source tree at
commit `eaa69e7` (2026-09-11), the GitHub releases API, Ubuntu 26.04 release notes, and
individual issues. Where a fact comes from code, the file is named so it can be re-checked.
Items marked **implement** are ready to build without further research.

## 1. S5 answered: why WSL 2.4.13 cannot boot Ubuntu 26.04

**Root cause (high confidence, VM confirmation still pending):** cgroup v1.

- Ubuntu 26.04 release notes: "Support for `cgroup` version 1 (`legacy` and `hybrid`
  hierarchies) has been removed." systemd was updated to 259. Ubuntu on WSL runs systemd
  as PID 1.
- WSL 2.5.1 (pre-release, 2025-03-12) changelog: "Remove cgroupv1 support" and "Update
  Kernel to 6.6.75". First stable release with that change: **2.5.7 (2025-04-24)**.
  Every 2.4.x runtime, including 2.4.13 (2025-03-20), still mounts a hybrid cgroup v1
  hierarchy for the distro.
- A systemd that refuses v1/hybrid cannot start under 2.4.x; `/init` fails to bring up
  the distro; `wslservice` surfaces that as `Wsl/Service/E_UNEXPECTED`. Nothing in the
  message names cgroups, which is why it took twelve commands to find.
- Source confirms the model: `src/linux/init/WslDistributionConfig.h:80` defaults
  `CGroup = CGroupVersion::v2`; `automount.cgroups = v1|v2` is the wsl.conf opt-in added
  in 2.6.2 ("allow distributions to opt-in to cgroupv1 mounts"); `src/linux/init/config.cpp`
  (around line 1827) falls back to v2 when the kernel command line has `cgroup_no_v1=all`.

**Consequences, implemented in this commit:**

- `compat.json` row for ubuntu 26.04 gets `min_runtime = 2.5.7` with the cause above.
  `known_bad_max = 2.4.13` and `known_good_min = 2.7.13` stay as the observed bounds.
- The citation of `microsoft/WSL#13484` as "the founding bug's public issue" was wrong
  and is removed. That issue (Ubuntu 22.04 on WSL 2.6.1, closed 2025-09-25) is the
  **same error string with a different cause**: a corrupted VHD, diagnosed from dmesg
  `EXT4-fs error ... iget: checksum invalid` and `/sbin/init: error while loading shared
  libraries: libc.so.6: file too short`. It belongs in the error dictionary (§2) as the
  second known cause of `Wsl/Service/E_UNEXPECTED`, and it is the motivating case for
  `DSK004` (read-only fsck via the system distro).

**Generalisation:** any distro whose systemd is 258 or newer is cgroup-v2-only. Rather
than one row per distro release, the matrix should carry a capability requirement
(`requires: ["cgroup_v2"]`) and a runtime capability table (`cgroup_v2_only_since: 2.5.7`).
Candidate rows once verified: Fedora 43+, Debian 14, Arch (rolling). Reverse hazard:
a distro that sets `automount.cgroups = v1` on a runtime older than 2.6.2 has the key
silently ignored (WSL005 lint rule).

**Kernel per runtime line** (for the matrix and for `WSL001` detail text): 2.4.x ships
5.15.167.4; 2.5.1 6.6.75; 2.5.7 6.6.87.1; 2.5.9 6.6.87.2; 2.7.5 first 6.18.

## 2. Error-code dictionary: exactly how `Wsl/Service/…/E_FOO` is built

`src/windows/common/wslutil.cpp`, `ErrorToString`: the error carries a 64-bit context
bitmask. The string is every set bit's name in **ascending bit order** joined by `/`,
then `/` and either a known name from `g_commonErrors` or `0x%08x`. The bit names are the
`Context` enum in `src/windows/common/ExecutionContext.h`:

```
Wsl Wslg Bash WslConfig InstallDistro EnumerateDistros Service RegisterDistro
CreateInstance AttachDisk DetachDisk CreateVm ParseConfig ConfigureNetworking
ConfigureGpu LaunchProcess ConfigureDistro CreateLxProcess UnregisterDistro
ExportDistro GetDistroConfiguration GetDistroId SetDefaultDistro SetVersion
TerminateDistro RegisterLxBus MountDisk Plugin MoveDistro GetDefaultDistro
DebugShell HCS HNS CallMsi Install ReadDistroConfig UpdatePackage
QueryLatestGitHubRelease VerifyChecksum WslC
```

So `Wsl/Service/CreateInstance/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED` means: client
`wsl.exe` → `wslservice` → creating an instance → creating the VM → Host Compute Service
call → that HRESULT. A parser is a split on `/`, a lookup per segment, and a final code.

**implement** `wsldoctor explain <code>` (M2):

| Segment(s) | Probes to run first | Notes |
|---|---|---|
| `HCS`, `CreateVm` | HST001, HST003, HST002 | `HCS_E_HYPERV_NOT_INSTALLED`, `HCS_E_SERVICE_NOT_AVAILABLE` (vmcompute stopped), `0x80370102` |
| `HNS`, `ConfigureNetworking` | NET004, NET006, HST host IPv6 | `0x8007054f` seen with mirrored fallback (#13454, open, 135 comments) |
| `AttachDisk`, `MountDisk` | DSK002, DSK003, DSK006 | `ERROR_ACCESS_DENIED` on attach: 2.5.6 added a `GrantVmAccess` fallback; VHD ownership after moves fixed 2.7.11/12 |
| `Plugin` | PLG001 | `WSL_E_PLUGIN_REQUIRES_UPDATE` = `MAKE_HRESULT(ERROR, ITF, 0x032A)` = `0x8004032A` |
| `ParseConfig`, `WslConfig` | WSL005 | `.wslconfig` or wsl.conf parse |
| `ReadDistroConfig` | preflight | `/etc/wsl-distribution.conf` in a `.wsl` file |
| `Install*`, `RegisterDistro`, `UpdatePackage`, `QueryLatestGitHubRelease`, `VerifyChecksum`, `CallMsi` | HST004, WSL003 | install-time; 2.6.1: "do not attempt to install distros if a reboot is required" |
| `Service` + `E_UNEXPECTED` with nothing deeper | WSL001, DSK002, then DSK004 | the bucket: runtime too old for the distro (§1), or corrupted rootfs (#13484) |
| `WslC` | none yet | containers, 2.9 pre-release |

Generation: a `go generate` script over the vendored source extracts the enum names,
`g_contextStrings`, and `g_commonErrors` into `internal/data/files/errors.json`; a test
fails when the enum grows. Known causes and probe mappings are hand-curated in the same file.

## 3. Configuration key tables straight from the parser

**.wslconfig** (`src/windows/common/WslCoreConfig.h`): 60 keys, versus 28 documented.
Documented ones match the docs table. Undocumented but accepted:

```
wsl2.crashDumpFolder wsl2.debugConsoleLogFile wsl2.dhcp wsl2.dhcpTimeout
wsl2.distributionStartTimeout wsl2.earlyBootLogging wsl2.gpuSupport
wsl2.hardwarePerformanceCounters wsl2.hostFileSystemAccess wsl2.isolateDistroCgroup
wsl2.kernelBootTimeout wsl2.kernelDebugPort wsl2.loadDefaultKernelModules
wsl2.loadKernelModules wsl2.macAddress wsl2.mountDeviceTimeout wsl2.systemDistro
wsl2.telemetry wsl2.virtio wsl2.virtiofs wsl2.vmSwitch
general.distributionInstallPath general.guiApplications general.instanceIdleTimeout
experimental.portRelay experimental.setVersionDebug experimental.swiotlb
experimental.virtioFsAggregateShares
```

The `[experimental]` names for `networkingMode`, `autoMemoryReclaim`, `sparseVhd`,
`dnsTunneling`, `firewall`, `autoProxy`, `ignoredPorts`, `hostAddressLoopback`,
`bestEffortDnsParsing`, `dnsTunnelingIpAddress`, `initialAutoProxyTimeout` are still
parsed as aliases, so "moved to [wsl2]" is a note, not a warning. Windows-11-only and
22H2-only gating comes from the docs footnotes, not the parser. 2.5.1 also introduced a
`DefaultNetworkingMode` **policy** value: a corporate policy can override `.wslconfig`
silently, which is a probe of its own (read the policy key, report the override).

**implement** WSL005 lint rules: unknown key; wrong section; `bridged` deprecated since
2.4.5; `vmSwitch`/`macAddress` without `bridged`; `memory=` above physical RAM;
`kernel=`/`kernelModules=`/`swapFile=` paths missing; `localhostForwarding` with
`mirrored` (ignored); `automount.cgroups=v1` on runtime < 2.6.2 (wsl.conf); Windows-11
keys on Windows 10; CRLF line endings on runtime < 2.5.1 (support added there).

**wsl.conf**: the init parser (`src/linux/init/WslDistributionConfig.cpp`) lists
`automount.cgroups automount.options boot.initTimeout fileServer.logFile fileServer.logLevel
fileServer.logTruncate filesystem.umask network.hostname user.default` through `ConfigKey`;
the rest (`automount.enabled/root/mountFsTab/ldconfig`, `boot.systemd/command/protectBinfmt`,
`interop.*`, `network.generateHosts/generateResolvConf`, `gpu.*`, `time.useWindowsTimezone`,
`fileserver.enabled`, `general.hostname`) are enumerated in `distributions/validate-modern.py`
`WSL_CONF_KEYS`. Union of both is the lint table.

**wsl-distribution.conf** keys parsed by init: `oobe.command`, `oobe.defaultName`,
`oobe.defaultUid`, `shortcut.enabled`, `shortcut.icon` (`ico` alias), `windowsterminal.enabled`,
`windowsterminal.profileTemplate`. This is the schema for `preflight` (§8).

## 4. Plugins: what actually breaks

`src/windows/service/exe/PluginManager.cpp`, `LoadPlugins`:

- Enumerates values of `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss\Plugins`.
  Value name = plugin name, value data = DLL path. Non-`REG_SZ` values are skipped with a
  logged `E_UNEXPECTED`; a second value pointing at the same DLL path is skipped.
- Official builds **validate the Authenticode signature** before `LoadLibrary`
  (`ValidateFileSignature`); the file handle is held open so it cannot be swapped.
- The DLL must export `WSLPluginAPI_EntryPointV1`; its return code is checked.
- Any failure is stored and thrown when the VM starts: "A fatal error was returned by
  plugin '{}'" or, for `WSL_E_PLUGIN_REQUIRES_UPDATE`, "The plugin '{}' requires a newer
  version of WSL. Please run: wsl.exe --update".

**implement** PLG001 v2: per value check type is `REG_SZ`, file exists, signature verifies
(`WinVerifyTrust`, works unelevated), export table contains `WSLPluginAPI_EntryPointV1`
(Go's `debug/pe` reads exports without loading the DLL), duplicate paths. Each failing
check maps to the exact user-visible message above, so `explain` can quote it.

## 5. Networking preconditions the runtime itself checks

`src/windows/service/exe/WslCoreVm.cpp`, `ValidateNetworkingMode`, and
`src/windows/common/WslCoreFirewallSupport.cpp`:

- **Hyper-V firewall support** is decided by: the registry value
  `HKLM\SYSTEM\CurrentControlSet\Services\MpsSvc\Parameters\HyperVFirewallDisable` (1 =
  off), then WMI in `ROOT\standardcimv2`: class `MSFT_NetFirewallHyperVVMCreator` present
  = v1 support (mirrored only), instances of `MSFT_NetFirewallHyperVProfile` = v2 (NAT
  too). If unsupported and the user set `firewall=` explicitly, WSL prints "Hyper-V
  firewall is not supported". Spike (`spikes/qfe`, Windows 10 22H2, unelevated): both
  queries return `Invalid class` in about 40 ms, which is precisely WSL's
  `HyperVFirewallSupport::None` branch, so wsldoctor can run the same two queries and
  explain the fallback. Windows 11 22H2+ runners are needed to see the v1/v2 answers.
- **Installed updates** via `Win32_QuickFixEngineering`: readable unelevated, 91 rows in
  0.96 s, a `WHERE HotFixID='KB…'` filter still costs 0.64 s. Acceptable for one
  query per run; keep it behind the "mirrored mode configured" condition.
- **Mirrored + IPv6 disabled by registry**: `Tcpip6\Parameters\DisabledComponents == 0xFF`
  makes mirrored unsupported ("Mirrored networking mode is not supported: {}. Falling back
  to NAT networking."). We already collect that value → **implement** NET004 rule now.
- **VPN detection as WSL does it** (`WslCoreNetworkingSupport.h:190`):
  `IsInterfaceTypeVpn` = `IF_TYPE_PPP` (23) or `IF_TYPE_PROP_VIRTUAL` (53); DNS resolution
  prefers a VPN interface; `IF_TYPE_TUNNEL` and loopback are skipped. Use
  `GetAdaptersAddresses` (in `x/sys/windows`) with the same rule for a "VPN adapter up"
  fact feeding NET002/NET004.
- **Open regressions to detect**: `#13724` (KB5068861, Windows 11 24H2, mirrored + VPN
  loses tunnel access, 88 comments, open) and `#13454` (`Failed to configure network
  (networkingMode Mirrored), falling back to networkingMode None`, code `0x8007054f`,
  135 comments, open). KB presence via `Win32_QuickFixEngineering` (spike below).
- `DefaultNetworkingMode` policy (2.5.1): report when policy overrides `.wslconfig`.

## 6. `fix defender` without PowerShell

`MSFT_MpPreference` in `root\Microsoft\Windows\Defender` exposes static methods `Add`,
`Remove`, `Set` (the cmdlets are thin wrappers). `Add` takes `ExclusionPath string[]`,
`ExclusionProcess string[]`, `ExclusionExtension string[]`. Via `go-ole`:
`svc.Get("MSFT_MpPreference")` → `Methods_.Item("Add").InParameters.SpawnInstance_()` →
set the array properties → `svc.ExecMethod("MSFT_MpPreference", "Add", params)`.
Requires elevation (already the plan). Rollback is `Remove` with identical arguments, so
the undo journal can replay it exactly. Caveat to surface in the plan text: on devices
managed by Defender for Endpoint with tamper protection covering exclusions, the call
succeeds but is reverted by policy; the fix should re-read and report "not applied by policy".
Exclusion set: each distro's `ext4.vhdx` (not the whole `%LOCALAPPDATA%\wsl`), processes
`vmmem`, `vmmemWSL`, `wslservice.exe`.

## 7. Zone.Identifier files (ZON001, `fix zone`)

`#7456` (399 reactions, open): saving a download from Edge or Explorer into
`\\wsl.localhost\…` writes the `:Zone.Identifier` alternate data stream; the plan9 server
has no ADS, so it lands as a **regular Linux file** named `<name>:Zone.Identifier`. Count
them by walking `\\wsl.localhost\<distro>\home` and `\root` over UNC (only when the
distro is running, never wake it), depth-limited. `fix zone` deletes those paths over UNC
with a dry-run count first. Windows-side `DrvFs` paths are unaffected.

## 8. Modern distro pre-flight (`wsldoctor preflight <file.wsl>`)

A `.wsl` is a tar (gzip or xz) with `/etc/wsl-distribution.conf` (§3 keys), `/etc/passwd`,
optionally `/etc/wsl.conf`. `distributions/validate-modern.py` encodes Microsoft's own
rules: default uid must exist in passwd or be creatable, uid 0 must be `root`, discouraged
systemd units (`systemd-resolved.service` and others) warned, xattrs unsupported on WSL1
(`security.selinux`, `security.ima`, `security.evm`), wsl.conf keys restricted to the known
list. Port the header-only subset to Go with `archive/tar` + `compress/gzip`; xz needs a
dependency (`ulikunitz/xz`) → separate ADR, or detect xz and say "unsupported yet".
Then apply the compat matrix: distro systemd 258+ and runtime < 2.5.7 → refuse with the §1
explanation before `wsl --install --from-file` ever runs.

## 9. Small probes now unblocked by data we already collect

- **HST004 pending reboot**: `Host.PendingReboot` is collected; 2.6.1 made WSL refuse to
  install distros while a reboot is pending. WARN when pending and no runtime or no distro.
- **NET004 IPv6 rule**: `Host.IPv6Disabled == 0xFF` with `networkingMode=mirrored` → FAIL.
- **WSL005 minimal**: parse `.wslconfig` (parser exists) and flag unknown keys against the
  60-key table.

## 10. Order of implementation suggested by this research

Each item is a GitHub issue in wslkit/wsldoctor.

1. compat.json S5 row and fixture correction (done in this commit); VM confirmation #1;
   capability model for the matrix #2.
2. `errors.json` generator + `explain` #3 (M2), because it reuses the compat and probe data.
3. `wslconfig-keys.json` from `WslCoreConfig.h` + WSL005 lint #4; wsl.conf lint #5.
4. NET004 #6 (IPv6 0xFF, Hyper-V firewall WMI classes, KB5068861 presence) and the VPN
   adapter fact + NET002 #7.
5. PLG001 v2 #8 (type, signature, export, duplicates).
6. `fix defender` via WMI `Add`/`Remove` #9; DEF001 elevated verification #10.
7. `preflight` for gzip `.wsl` #11; xz behind an ADR.
8. ZON001 and `fix zone` over UNC #12.
9. Smaller: HST004 #13, DSK004 #14, DSK006 #15, EVT001 elevated + running signal #16,
   HST005 CLSIDs #17, `--online` #18. Release: signing #19, Scoop/winget #20.

## Sources

- WSL source, commit eaa69e7: `src/windows/common/WslCoreConfig.h`, `ExecutionContext.h`,
  `wslutil.cpp`, `WslCoreFirewallSupport.cpp`, `WslCoreNetworkingSupport.h`,
  `src/windows/service/exe/WslCoreVm.cpp`, `PluginManager.cpp`,
  `src/linux/init/WslDistributionConfig.{h,cpp}`, `config.cpp`, `src/shared/inc/lxinitshared.h`,
  `distributions/validate-modern.py`, `localization/strings/en-US/Resources.resw`
- WSL 2.5.1 release: https://github.com/microsoft/WSL/releases/tag/2.5.1
- WSL 2.6.2 release (cgroupv1 opt-in): https://github.com/microsoft/WSL/releases/tag/2.6.2
- Ubuntu 26.04 LTS release notes: https://documentation.ubuntu.com/release-notes/26.04/summary-for-lts-users/
- Corrupted-VHD E_UNEXPECTED case: https://github.com/microsoft/WSL/issues/13484
- KB5068861 mirrored+VPN: https://github.com/microsoft/WSL/issues/13724
- Mirrored `Failed to configure network` 0x8007054f: https://github.com/microsoft/WSL/issues/13454
- Zone.Identifier: https://github.com/microsoft/WSL/issues/7456
- Defender WMI class: https://learn.microsoft.com/en-us/powershell/module/defender/add-mppreference
