# Cgroup v1 and a distro that will not boot, observed

`compat.json` says Ubuntu 26.04 needs WSL 2.5.7 or newer because the distribution is
cgroup-v2-only and older runtimes mount a hybrid cgroup v1 hierarchy. That was read out
of release notes and source (`2026-09-feature-research.md` §1, ADR 0007), not watched
happening. This is the mechanism half of that claim, watched happening.

Machine: Windows 10 Pro 19045, WSL **2.9.11**, Ubuntu 26.04.1 LTS with systemd 259.
Date: 2026-09-13. Nothing here needed an old runtime, and nothing here touched the
working distribution: every boot below is a throwaway copy, imported from
`wsl --export Ubuntu ubuntu2604.tar` and unregistered afterwards.

## What was run

WSL 2.6.2 added `automount.cgroups`, a per-distribution opt-in to the hierarchy that was
removed in 2.5.1. That key is a time machine: it puts a current runtime into the state an
old one was in, for one distribution, with no MSI to install and nothing else on the
machine affected.

| # | `/etc/wsl.conf` | `.wslconfig` | Result |
|---|---|---|---|
| 1 | `systemd=true` | none | boots; `/sys/fs/cgroup` is `cgroup2fs` |
| 2 | `systemd=true`, `cgroups=v1` | none | **fails**, `Wsl/Service/E_UNEXPECTED` |
| 3 | `systemd=true`, `cgroups=v1` | `isolateDistroCgroup=false` | **fails**, `Wsl/Service/E_UNEXPECTED` |
| 4 | `systemd=false`, `cgroups=v1` | `isolateDistroCgroup=false` | boots; `/sys/fs/cgroup` is `tmpfs` with v1 controllers |

Rows 3 and 4 differ in one thing: whether PID 1 is systemd. Same image, same runtime,
same v1 hierarchy. That is the experiment.

## What the kernel log says

Row 2, with per-distribution cgroup isolation left at its default, never gets as far as
the distribution's init. WSL says so on the console — "Cgroup v1 is incompatible with
per-distribution cgroup isolation" — and `wsl --system -u root -- dmesg` shows its own
mount failing:

```
/proc/cgroups lists only v1 controllers, use cgroup.controllers of root cgroup for v2 info
WSL (1 - ) ERROR: UtilMount:1846: mount(cgroup, /sys/fs/cgroup/cpuset, cgroup, 0x20000e, cpuset) failed 16
WSL (1) ERROR: Resource busy @config.cpp:1903 (ConfigInitializeCgroups)
WSL (1 - init-distro) ERROR: InitEntryUtilityVm:2343: Cgroup path /sys/fs/cgroup/wsl-user/distro-7271 does not exist
WSL (1 - init()) ERROR: InitEntryUtilityVm:2617: Init has exited. Terminating distribution
```

That is a second, newer way to reach the same error code, and it is not the one the compat
row is about.

Row 3 turns the isolation off, the v1 hierarchy mounts cleanly, and then:

```
EXT4-fs (sde): mounted filesystem 43a33f74-… r/w with ordered data mode. Quota mode: none.
/proc/cgroups lists only v1 controllers, use cgroup.controllers of root cgroup for v2 info
WSL (1 - init()) ERROR: InitEntryUtilityVm:2617: Init has exited. Terminating distribution
EXT4-fs (sde): unmounting filesystem 43a33f74-….
```

No mount error. The hierarchy is there, PID 1 starts, PID 1 exits, the distribution is
torn down, and the user gets `Catastrophic failure / Error code: Wsl/Service/E_UNEXPECTED`
with nothing else. Row 4 then boots the same disk with the same v1 hierarchy and only
`systemd=false` changed, and `stat -fc %T /sys/fs/cgroup` reports `tmpfs` with `blkio`,
`cpu`, `cpuacct`, `cpuset` and `memory` in it.

systemd's own account of why it gave up could not be captured: WSL does not relay PID 1's
stderr to the console, and `systemd --system --test` prints nothing when it is not PID 1.

## What this does and does not settle

Settled: a cgroup-v2-only systemd (259, as shipped in Ubuntu 26.04) cannot run as PID 1
when the only hierarchy present is cgroup v1, the distribution dies at init, and the user
sees `Wsl/Service/E_UNEXPECTED` and no other diagnostic. The mechanism in `compat.json` is
real, and the symptom it predicts is the symptom.

Not settled: that a 2.4.x runtime mounts exactly this hierarchy. The v1 hierarchy here was
produced by 2.9.11 honouring `automount.cgroups=v1`, not by an old runtime doing it
unasked. The version boundary in the compat row — `known_bad_max` 2.4.13, `min_runtime`
2.5.7 — still rests on release notes.

Also worth recording: on 2.9.11 the `automount.cgroups=v1` opt-in fails outright unless
`isolateDistroCgroup=false` is also set. A `.wslconfig`/`wsl.conf` lint has a rule to find
there, and it is one nobody can discover from the error code.

## Pinning the version boundary

What is left of wslkit/wslkit#1 needs a machine that can hold an old runtime, which is a
scratch VM. The procedure, to be run on a host with Hyper-V:

```powershell
# 1. A VM with nested virtualisation, from a Windows 11 24H2 ISO.
$vm  = 'wsl-bisect'
$vhd = "$env:USERPROFILE\vms\$vm.vhdx"
New-VM -Name $vm -Generation 2 -MemoryStartupBytes 8GB -NewVHDPath $vhd -NewVHDSizeBytes 80GB
Set-VMProcessor  -VMName $vm -Count 4 -ExposeVirtualizationExtensions $true
Set-VMMemory     -VMName $vm -DynamicMemoryEnabled $false
Add-VMDvdDrive   -VMName $vm -Path "$env:USERPROFILE\iso\Win11_24H2.iso"
Set-VMFirmware   -VMName $vm -FirstBootDevice (Get-VMDvdDrive -VMName $vm)
Start-VM $vm     # install Windows, then take a checkpoint
Checkpoint-VM -Name $vm -SnapshotName 'clean'

# 2. Inside the VM, per runtime under test. Features first, then the MSI, then the distro.
dism /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart
dism /online /enable-feature /featurename:Microsoft-Windows-Subsystem-Linux /all /norestart
# reboot, then:
msiexec /i wsl.2.4.13.0.x64.msi /qn      # from github.com/microsoft/WSL/releases
wsl --install --from-file ubuntu-26.04.wsl
wsl -d Ubuntu-26.04 -- echo ok           # expect Wsl/Service/E_UNEXPECTED
wslkit doctor check --json > 2.4.13.json
wsl --system -u root -- dmesg > 2.4.13.dmesg.txt

# 3. Restore the checkpoint and repeat for 2.5.1, 2.5.4, 2.5.6, 2.5.7.
Restore-VMSnapshot -VMName $vm -Name 'clean' -Confirm:$false
```

The first runtime that boots the distribution is the boundary. Capture `doctor check
--json` on the failing one, redact it, and it becomes a snapshot case beside
`testdata/snapshots/ubuntu-2604-on-2.4.13`, which today is the real failure captured on the
original machine with the runtime strings rewritten.
