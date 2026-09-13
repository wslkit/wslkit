# Future wslkit subcommands: mechanism research (2026-09-12)

Follow-up to issue #29 ("Feature ideas: future wslkit subcommands mined from microsoft/WSL
pain points"). #29 ranks the pain; this document checks each proposed mechanism against
the WSL source (microsoft/WSL at `eaa69e7`, paths relative to `src/`), the virtdisk and
WinHTTP documentation, upstream issue threads (comments through September 2026), prior
art, and two live spikes on this machine (Windows 10 22H2, WSL 2.7.13, unelevated).
Each section ends with a design that is ready to become an issue. Uncertain items are
marked as such.

## 0. Cross-cutting: the guest agent

Five of the seven Tier 1 items need a wslkit helper running inside the VM or a distro.
The routes, with what the source says about each:

| Route | Where it runs | Admin | Persistence | Evidence |
|---|---|---|---|---|
| `wsl.exe -d <distro> -u root -- <cmd>` | inside the distro; root there is root in the VM (no user namespace) | none | per invocation; wakes the VM | `linux/init/main.cpp:2334` (namespaces are `CLONE_NEWNS|NEWPID|NEWUTS` only) |
| systemd user/system unit installed into the distro | inside the distro | none | survives restarts | how wsl2-ssh-agent, wsl-gpg-systemd, WslNotifyd install |
| `wsl --system` | the **system distro** (own mount/pid ns), not the VM root; sees the whole cgroup tree | none, needs GUI apps enabled | per invocation | `windows/common/WslClient.cpp:1763`, `LxssUserSession.cpp:2793` |
| `wsl --debug-shell` | VM root, as a TTY (`agetty -a root` on a virtio-serial port) | effectively admin (pipe ACL) and policy-gated (`AllowDebugShell`) | interactive only | `WslClient.cpp:1483-1512`, `main.cpp:954` |
| plugin `ExecuteBinary` | VM root (`wsl-user/non-distro` cgroup) | admin to register under `HKLM\...\Lxss\Plugins`, Authenticode required | for the VM's life | `WslPluginApi.h:121`, `PluginManager.cpp:78-85, 419` |

Decision for the designs below: **a static Linux helper copied into each distro and
started by a systemd unit (or a profile.d launcher when systemd is off), controlled from
Windows over Hyper-V sockets.** Root-namespace execution is not needed by any Tier 1
item once the cgroup finding in §2 is taken into account; the plugin route stays a
later, enterprise-flavoured option.

### Spike: Hyper-V sockets from an unelevated Windows process

`spikes/hvsock` (go-winio `HvsockAddr` with `VsockServiceID(port)`, the same template
GUID scheme WSL uses in `windows/common/hvsocket.cpp:22-29`), against the running VM
whose id came from `wsl -d Ubuntu -- wslinfo --vm-id` (`linux/init/wslinfo.cpp:150`).

| Direction | Result |
|---|---|
| Windows **listens** on an unregistered template port bound to the VM id; guest `AF_VSOCK` connects to CID 2 | **works unelevated**, no `GuestCommunicationServices` registry entry. WSL itself registers nothing there (zero source references). |
| Guest listens on `VMADDR_CID_ANY:<port>`; Windows user-mode **connects** | **timed out** (WSAETIMEDOUT), twice. WSL's own `wslhost.exe` does exactly this under the user token (`interop.cpp:260-320`), and its `Connect` adds only `HVSOCKET_CONNECTED_SUSPEND` and an explicit `HVSOCKET_CONNECT_TIMEOUT` (`hvsocket.cpp:40-78`). Cause not identified; open item. |

Consequence: build the control plane on **guest-initiated** connections (Windows listens),
which is also the natural direction for every preset in #29. Host-initiated flows
multiplex over that channel. The VM id changes on every `wsl --shutdown` (microsoft/WSL#5751),
so the agent re-reads it and reconnects.

## 1. `sock`: Windows <-> Linux socket bridge

**Verified.** All WSL channels are AF_HYPERV <-> AF_VSOCK streams; the service listens on
fixed ports 50000-50006 (`lxinitshared.h:115-121`), everything else is "Linux listens on
an ephemeral port and tells Windows" (`binfmt.cpp:150-161`, `interop.cpp:260-320`).
`CONFIG_VSOCKETS=y`, `CONFIG_HYPERV_VSOCKETS=y` in the WSL kernel; `/dev/vsock` present;
works with interop disabled and in any networking mode. Cross-boundary `AF_UNIX` is
explicitly unsupported upstream (microsoft/WSL#5961). Windows OpenSSH and 1Password both
serve `\\.\pipe\openssh-ssh-agent` (mutually exclusive). gpg4win "sockets" are files
holding a TCP port plus a 16-byte nonce. Notifications: WslNotifyd implements
`org.freedesktop.Notifications` over D-Bus and launches a Windows companion via interop;
recent #2466 comments (2026) point at it; no WSLg plan.

**Design.** `wslkit sock serve` (Windows, user mode, no admin) listens on one template
port; `wslkit-sockd` (static Linux binary in `/usr/local/lib/wslkit`, systemd user unit)
connects and multiplexes logical streams. Presets, all Linux-client to Windows-service:

| Preset | Linux endpoint | Windows endpoint |
|---|---|---|
| `ssh-agent` | `$XDG_RUNTIME_DIR/wslkit/ssh-agent.sock`, `SSH_AUTH_SOCK` via profile.d | `\\.\pipe\openssh-ssh-agent`; translate OpenSSH 8.9+ extension messages as wsl2-ssh-agent does |
| `gpg-agent`, `.ssh`, `.extra` | `/run/user/<uid>/gnupg/S.gpg-agent*` | Assuan file -> `127.0.0.1:<port>` + nonce, as albertony/npiperelay `-a` |
| `notify` | D-Bus `org.freedesktop.Notifications` (activation file) | WinRT toast with an HKCU `AppUserModelId`; semantics from WslNotifyd |
| raw | user AF_UNIX path | user named pipe |

Fallbacks: interop stdio (`wslkit.exe sock stdio <preset>` spawned from Linux, one
process per connection) when vsock is unavailable; TCP localhost with a nonce last.
Doctor findings to add: interop disabled, both agents claiming the pipe, missing gpg
socket dir, stale VM id. Link rather than rebuild: wsl2-ssh-agent (extension translation),
albertony/npiperelay (Assuan), WslNotifyd (toasts), VcXsrv `-wslvsock` (rebind on VM id
change).

**Open:** the host-connect failure above; whether one multiplexed connection or one
vsock stream per logical socket performs better for ssh-agent's short exchanges.

## 2. `limit` and `top`: per-distro cgroups

**Verified.** `wsl2.isolateDistroCgroup` defaults to **true** (`WslCoreConfig.h:386`).
With it, `SetupWslUserCgroup` (`linux/init/main.cpp:3834-3928`) enables `cpu` and
`memory` in the root `cgroup.subtree_control`, creates `/sys/fs/cgroup/wsl-user` with
`memory.max = totalram - 32 MiB` and `cpu.max = (nproc*100000 - 1000) 100000`, plus
`wsl-user/non-distro`. Per distro start, mini_init creates `wsl-user/distro-<pid>` where
`<pid>` is the distro init's pid in the root pid namespace (`util.cpp:3992-3995`,
`main.cpp:2261-2264`); with `boot.systemd=true` it adds `distro-N/systemd` and
`distro-N/non-systemd`. The path reaches the distro through `WSL2_DISTRO_CGROUP_PATH`
(`main.cpp:1577`) and init moves systemd, session leaders and boot commands into it
(`init.cpp:2421-2424, 1209, 1269`). The subtree is removed when the distro exits
(`main.cpp:4249-4265`). **There is no cgroup namespace** (`CLONE_NEWCGROUP` appears
nowhere), and each distro mounts the full cgroup2 hierarchy with `nsdelegate`
(`config.cpp:1862-1864`), so any distro sees every other distro's `wsl-user/distro-*`.
No per-distro limits exist anywhere in WSL today.

`autoMemoryReclaim` (`util.cpp:3783-3990`) polls `/proc/stat` every 10 s and acts only
when CPU is under 0.5% busy for two minutes; `gradual` writes `memory.reclaim` in
256 MiB to 1 GiB steps keeping a 128 MiB file-cache floor, `dropCache` writes
`drop_caches=3`; both then `compact_memory=1`. **Only file cache is reclaimed; anonymous
memory never returns to the host.**

**Design.**
- `limit set <distro> --memory 4G --cpu 2`: run `cat /proc/self/cgroup` in the distro to
  learn `/wsl-user/distro-N/...`, then write `memory.max`, `memory.high`, `cpu.max` into
  `/sys/fs/cgroup/wsl-user/distro-N` as root. No Windows admin. Preconditions:
  `isolateDistroCgroup` not false, distro on cgroup v2, VM has `cgroup.controllers`.
  Limits vanish when the distro restarts (new pid, subtree recreated), so persistence is a
  small hook: a `boot.command` line or a systemd unit that calls `wslkit-limitd apply`,
  reading `/etc/wslkit/limits.toml`.
- `top`: per-distro totals from `wsl-user/distro-*/memory.current`, `memory.stat`
  (anon vs file), `cpu.stat`, read from any one running distro; map `distro-N` to a name
  by reading `/proc/1/cgroup` in each running distro (one launch each). Per-process
  attribution only for the distro the reader runs in (`cgroup.procs` shows other distros'
  pids as 0). Explain vmmem: total = root `memory.current`; unreclaimable = anon; show
  whether the 0.5%/2 min idle gate is currently satisfied.

**Open:** effective cap is min(distro, `wsl-user`, VM memory); `memory.max` below current
usage OOM-kills inside the distro only; behaviour when a user sets
`isolateDistroCgroup=false` (both commands should say so and stop).

## 3. `guard`: sleep and resume recovery

**Verified.** WSL handles no power events: the generic service helper registers only
`GUID_LOW_POWER_EPOCH_PRV` and only when a service implements `OnLowPowerModeChanged`,
which `WslService` does not (`windows/inc/comservicehelper.h:443-449, 589-621`;
`ServiceMain.cpp:60, 232`); `PBT_APMSUSPEND`/`PBT_APMRESUMEAUTOMATIC` fall through to
`default`. The Linux init has no resume hook. `vmIdleTimeout` is purely activity based
(`LxssUserSession.cpp:3977-4008`). `wsl --shutdown --force` exists (`windows/inc/wsl.h:102`)
and calls `HcsTerminateComputeSystem` directly (`LxssUserSession.cpp:2204-2225`). Time
inside the VM is kept by chronyd against the Hyper-V PTP clock with `makestep 1.0 3`
(`main.cpp:3214, 3694-3700`): after the first three updates chrony **slews**, so a large
offset after hours of sleep can persist (inference).

Upstream (2024-2026 comments on #8696, #12747, #8763, #14005): `wsl --shutdown` works
when the service still answers and hangs when vsock is dead; `taskkill /f /im
wslservice.exe` (admin) is the most-reported working step; `Restart-Service` is mixed;
**stopping `vmcompute` produced a BSOD** (#8696, 2025-12-31) and is excluded; a
contributor reports a partial fix in 2.7.3 pre-release while #14005 still reproduces on
2.9.9; the best root-cause description is AF_HYPERV sockets torn down at Modern Standby
entry and never restored (#14005, 2026-05-28). No maintainer statement.

**Design.** `wslkit guard install` registers a per-user scheduled task on Kernel-Power
event 107, Power-Troubleshooter event 1 and logon, running `wslkit guard run-once`;
`--elevated` adds a second task with highest privileges (admin once). `run-once` waits
15-20 s, probes `wsl --status` (10 s) then `wsl -d <default> -e true` (20 s), compares
guest and host time; ladder: `wsl --shutdown` (30 s) -> `wsl --shutdown --force` (30 s)
-> elevated only: `taskkill /f /im wslservice.exe`, re-probe -> `Restart-Service
WSLService`. Never touches `vmcompute` or `HvHost`. Escalates only when the VM was running
before suspend (resident mode records `wsl -l --running` at `PBT_APMSUSPEND` via
`PowerRegisterSuspendResumeNotification`, no admin) or when `wsl --status` itself hangs;
a slow first start after a full shutdown is not a failure.

**Open:** whether `--force` succeeds with dead vsock; whether Modern Standby (S0ix) emits
event 107; whether killing `wslservice.exe` loses unsaved distro state.

## 4. `proxy`: PAC-aware, persistent proxy configuration

**Verified.** `autoProxy` uses `WinHttpGetProxySettingsEx(WinHttpProxySettingsTypeWsl)`
under the user's token (`windows/service/exe/LxssHttpProxy.cpp:58-59, 467, 505, 530`),
not per-URL PAC evaluation. Values are injected **per process** into every
`CreateLxProcess` environment (`LxssUserSession.cpp:825, 4199-4237`), never written to a
file, and WSLENV wins over them (`config.cpp:1615-1660`); systemd services and
`[boot] command` never see them. PAC is passed through as `WSL_PAC_URL` only ("PAC is not
functional in headless Linux", `:4221-4225`). **The #40945 gap (HTTPS_PROXY empty with a
PAC) was fixed upstream by PR #40950 (merged 2026-07-03, in 2.9.x): HTTPS falls back to
the HTTP proxy.** On a proxy change WSL only toasts "please restart WSL" (`:262-266`); new
processes get new values, running ones do not. In NAT mode, loopback and IPv6 proxies are
**dropped** (`:345-371`, `LxssUserSession.cpp:4066-4084`), so a Windows-side proxy on
127.0.0.1 is invisible to autoProxy there. wslc has no proxy code at all (#40981 open).

Linux reaches the host at the NAT gateway (`HKLM\...\Lxss\NatGatewayIpAddress`,
`WslCoreConfig.cpp:224`) or at 127.0.0.1 in mirrored mode. `px` already implements the
Windows-side per-URL PAC forward proxy with SSPI auth (`WinHttpGetIEProxyConfigForCurrentUser`
+ `WinHttpGetProxyForUrl`); `alpaca` does PAC on the Linux side; `cntlm` is NTLM only.

**Design.** `wslkit proxy serve`: user-mode forward proxy bound to the NAT gateway IP and
127.0.0.1 on a fixed port, resolving upstream per (scheme, host, port) through
`WinHttpGetProxyForUrl` with a TTL cache, CONNECT tunnelling, re-resolve on
`WinHttpRegisterProxyChangeNotification`. Prefer embedding or wrapping `px` for NTLM/Kerberos
rather than reimplementing. `wslkit proxy apply <distro>` writes `/etc/environment`,
`/etc/profile.d/99-wslkit-proxy.sh`, `/etc/apt/apt.conf.d/99wslkit-proxy`,
`~/.docker/config.json` proxies, `/etc/systemd/system.conf.d/wslkit-proxy.conf`, plus
`/etc/wslkit/proxy.env` for units to `PathChanged` on (answers #14152). Recommend
`wsl2.autoProxy=false` when the local proxy is in use. Doctor finding for a firewall
blocking inbound on the vEthernet adapter (rule creation needs admin).

**Open:** whether Defender Firewall blocks the vEthernet inbound path by default; gateway
IP changes across reboots (watch the registry value).

## 5. `disk`: snapshots, trash-on-unregister, and the wsldisk port

**Verified.** WSL never calls `AttachVirtualDisk`; it hands the path to HCS as a SCSI
attachment (`windows/common/hcs.cpp:56-69, 212-228`) after `HcsGrantVmAccess` on that one
file, retrying once on `ERROR_ACCESS_DENIED` (`WslCoreVm.cpp:1093-1114`). **Nothing in the
source knows about parents or differencing disks.** `--manage --move` moves the VHD file
only and rewrites `BasePath`/`VhdFileName` (`LxssUserSession.cpp:948-998`); `--resize`
grows via `ResizeVirtualDisk` then `e2fsck` + `resize2fs` in the utility VM, shrinks via
`resize2fs` first then `ALLOW_UNSAFE_VIRTUAL_SIZE` (`:1831-1863`); `--compact` runs
`e2fsck -E discard` in the VM then `CompactVirtualDisk` detached (`:1869-1914`);
`--set-sparse` is `FSCTL_SET_SPARSE` behind `--allow-unsafe` (`:1777-1804`).
`--unregister` has **no confirmation**, deletes the VHD (also for in-place imports) and
fires `OnDistributionUnregistered` only **after** deletion (`WslClient.cpp:1254-1263`,
`LxssUserSession.cpp:3065-3176, 2502`). `--mount --vhd --bare` attaches a VHD without
starting a distro and needs no elevation (`WslClient.cpp:1008-1045`, `WslCoreVm.cpp:1866-1877`).
virtdisk: compact needs `METAOPS` and a detached disk in agnostic mode (wsldisk measured
100% reclaim unelevated after trim); differencing creation, merge semantics and
`OPEN_VIRTUAL_DISK_FLAG_NO_PARENTS` are documented but every WSL operation would act on
the child alone. wsldisk (README readable) already ships `list`, `info`, `compact`,
`trim`, `orphans`, `move`, `relink`, `usage`; plans `shrink`, `grow`, `snapshot`,
`clone`, `mount`; has **no trash/unregister safety net**.

**Design.** Common preconditions: WSL 2, distro stopped and VHD handle free (offer
`--shutdown`), `.vhdx`. Commands and risk: `info` (no risk), `compact` (delegate to
`wsl --manage --compact` when present, else trim -> stop -> `CompactVirtualDisk`), `move`
(delegate when available, else copy -> verify -> repoint -> smoke start -> delete),
`rename`, `resize` (delegate; **require a snapshot before shrink**), `sparse` (delegate,
default off, explicit warning), `trash` (stop -> export registration to JSON -> move VHD
and extras to `<trash>/<guid>/` -> `wsl --unregister`, which now deletes nothing;
`undelete` = `--import-in-place` plus restored `DefaultUid`/`Flags`), `snapshot` /
`restore` by **copy**, inspectable via `wsl --mount --vhd --bare`. Differencing disks
only as an experimental flag after measuring HCS attach of a child, because
`GrantVmAccess` is applied to the leaf only (parent access uncertain) and every WSL
operation would break the chain.

## 6. `watch`: file-change notifications

**Verified.** inotify events are kernel-generated by real operations on the watched
inode; nothing in user space can synthesise them for a 9p mount. Upstream #4739 was
locked by a maintainer in October 2024 ("use the WSL filesystem"); a 2026 reproduction
shows the gap also covers container bind mounts. Workarounds that work today are polling
knobs (`CHOKIDAR_USEPOLLING=1`, vite `server.watch.usePolling`, `ENTR_INOTIFY_WORKAROUND`,
`inotifywait-polling`). WinFsp exposes `FspFileSystemNotify` (since WinFsp 2021) for the
Linux-to-Windows direction.

**Design.** Not a wslkit subcommand. Windows-to-Linux: a wsldrive feature that performs a
real metadata touch on the changed file inside the served mount (mtime churn and
feedback loops are the cost). Linux-to-Windows: wsldrive raises WinFsp notifications.
wslkit contributes one doctor finding: a watcher-heavy tool running against `/mnt/c`
with the polling knobs absent, with the exact env var to set.

## 7. `log`: journal to Event Log

A non-admin process can write to the Application log through an existing event source;
a proper `wslkit` source needs a one-time `HKLM\SYSTEM\CurrentControlSet\Services\EventLog`
registration (admin), the same install pattern as `guard --elevated`. Forwarder shape:
`journalctl -o json -f` in the distro through the `sock` channel to a Windows writer.
Low priority; enterprise-flavoured.

## Suggested order (tracked as issues)

1. Guest-agent contract and the `sock` control channel (Windows listens; proven): #30.
2. `sock` presets: ssh-agent, then gpg, then notify: #31.
3. `limit` + `top` (WSL already isolates; small): #32.
4. `guard` (largest pain; needs the elevated task for the last rungs): #33.
5. `disk trash` (smallest, prevents the worst outcome): #34; then the wsldisk port with copy
   snapshots: #35.
6. `proxy` (corporate; wrap px): #36.
7. `watch`: doctor finding #37, forwarding itself inside wsldrive; `log` when an enterprise user asks.

## Sources

- microsoft/WSL source at eaa69e7: files cited inline.
- microsoft/WSL issues #2466, #4739, #5751, #5961, #8696, #8763, #11207, #12469, #12747,
  #14005, #14152, #40945, #40950 (PR), #40981; microsoft/WSL2-Linux-Kernel `config-wsl`.
- virtdisk API: CompactVirtualDisk, CreateVirtualDisk, MergeVirtualDisk, ResizeVirtualDisk,
  OpenVirtualDisk flags (learn.microsoft.com).
- WinHTTP: WinHttpGetProxySettingsEx, WinHttpGetProxyForUrl, WinHttpGetIEProxyConfigForCurrentUser.
- Power: PowerRegisterSuspendResumeNotification.
- Prior art: jstarks/npiperelay, albertony/npiperelay, mame/wsl2-ssh-agent,
  demonbane/wsl-gpg-systemd, ultrabig/WslNotifyd, stuartleeks/wsl-notify-send,
  genotrance/px, samuong/alpaca, sakai135/wsl-vpnkit, WinFsp release notes, X410 and VcXsrv
  vsock notes.
- wslkit/wsldisk README, PLAN, RESEARCH (via the GitHub API).
