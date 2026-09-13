# Per-distribution memory and CPU attribution in WSL 2

Written 2026-09-13, for `wslkit top` and the `limit` half of issue #32.

The design in that issue assumed that reading cgroup accounting from inside any
distribution attributes memory and CPU per distribution. On the machine this was
built on, WSL **2.7.13**, it does not: every distribution reads the same VM-wide
numbers. The mechanism the issue described is real, but it arrived in a later
WSL than the one here.

This records what was measured, what the WSL source says, and what `top`
therefore does.

## Measured, on WSL 2.7.13

Two distributions, Ubuntu with systemd and an Alpine-based one.

**Neither has `/sys/fs/cgroup/wsl-user`.** `test -d` says no in both.

**`/proc/1/cgroup` shows the root.** Alpine reports `0::/`; Ubuntu reports
`0::/init.scope`, its own systemd hierarchy created directly at the VM-wide
root.

**`/sys/fs/cgroup/memory.current` does not exist.** That is correct kernel
behaviour for the cgroup v2 root, and it is the tell that what a distribution
sees is the real root and not a namespaced subtree.

### The decisive experiments

Summing `memory.current` across the visible child cgroups initially looked like
per-distribution attribution: Ubuntu reported about 780 MB and Alpine about
820 MB, and the two summed to roughly the VM's committed memory. That was a
coincidence of two distributions being similarly sized.

**Burning CPU in one distribution moves every distribution's counter.** Five
seconds of busy loop inside Ubuntu only:

| | before | after | delta |
|---|---|---|---|
| Ubuntu | 58,214,216 us | 63,279,986 us | 5,065,770 us |
| Alpine | 58,313,377 us | 63,341,666 us | 5,028,289 us |

**Allocating memory in one distribution moves every distribution's counter.**
400 MB held in `/dev/shm` inside Ubuntu only:

| | before | after | delta |
|---|---|---|---|
| Ubuntu | 836,440,064 | 1,246,027,776 | 391 MB |
| Alpine | 845,144,064 | 1,243,987,968 | 380 MB |

Both distributions are reading the same cgroups. Cgroup accounting read from
inside a distribution on this WSL is **VM-wide**.

### What is genuinely per-distribution

The process list. Each distribution has its own PID namespace, so it sees only
its own processes:

| | processes | resident set |
|---|---|---|
| Ubuntu | 52 | 405 MB |
| Alpine | 13 | 130 MB |

## What the WSL source says

Read against the microsoft/WSL tree at tag **2.9.12** (commit `eaa69e7`).

The `wsl-user` machinery exists there, and the original assumption describes it
accurately:

- `wsl2.isolateDistroCgroup` defaults to **true**
  (`src/windows/common/WslCoreConfig.h:386`), is parsed from `.wslconfig`
  (`WslCoreConfig.cpp:113`) and is sent to the guest in the early-config message
  (`src/windows/service/exe/WslCoreVm.cpp:569`).
- mini_init acts on it once, at `src/linux/init/main.cpp:3127`, and only if the
  cgroup2 root has `cgroup.controllers`. It then creates
  `/sys/fs/cgroup/wsl-user` and sets a VM-wide `memory.max` of total RAM less
  32 MiB and a `cpu.max` of all processors less 0.01
  (`main.cpp:3834-3928`).
- Each distribution gets `/sys/fs/cgroup/wsl-user/distro-<pid>`
  (`main.cpp:2258-2286`), where `<pid>` is the **VM-root PID** of that
  distribution's init (`src/linux/init/util.cpp:3992-3995`), with `systemd` and
  `non-systemd` children for a systemd distribution.
- The path is handed to the distribution as `WSL2_DISTRO_CGROUP_PATH`
  (`src/shared/inc/lxinitshared.h:277`).

Two parts of the assumption hold in every version:

- **There is no cgroup namespace.** `CLONE_NEWCGROUP` appears nowhere in the
  tree. A distribution is given mount, PID, UTS and sometimes IPC namespaces
  (`main.cpp:3089`), and shares the VM's cgroup namespace. Isolation is by path
  convention and directory permissions, not by namespace.
- **Root in a distribution is root in the VM.** `CLONE_NEWUSER` appears nowhere
  either.

So where `wsl-user` exists, one distribution really can read every other one's
`memory.current`, and WSL's own test suite depends on it: `ValidateIsolatedCgroupLayout`
enumerates every distribution's cgroup from inside one of them
(`test/windows/UnitTests.cpp:7953-8012`).

And the observations here match that suite's expectations for the *other*
branch, `isolateDistroCgroup=false` (`UnitTests.cpp:8024-8043`), which asserts
`/proc/self/cgroup` is `0::/` and that `/sys/fs/cgroup/wsl-user` does not exist.

The source does not contradict the measurements. It post-dates them. `wsl-user`
is present at 2.9.12 and absent at 2.7.13, so it landed somewhere in 2.8.x to
2.9.x; the clone is shallow, so the exact release could not be pinned.

## There are no per-distribution limits

Worth stating plainly, because the `limit` half of issue #32 assumed otherwise.

`memory.max` is written in exactly one place in the whole WSL source,
`main.cpp:3904`, on `wsl-user`, VM-wide. `cpu.max` likewise, at
`main.cpp:3921`. The `distro-<pid>` cgroups are created and populated but are
never given `memory.max`, `memory.high`, `cpu.max` or `cpu.weight`.
`UtilEnableAllCgroupControllers` (`util.cpp:3997-4035`) only writes
`cgroup.subtree_control`, which makes controllers available without setting
anything.

The `wsl-user` cap is an OOM firewall, not accounting: it reserves 32 MiB and a
hundredth of a processor for mini_init and its helpers so a runaway workload
triggers a cgroup-local OOM kill rather than a VM-wide one (`main.cpp:3840-3851`).

`.wslconfig` `memory=` and `processors=` remain the only supported controls, and
they are VM-wide, applied to the compute topology at
`WslCoreVm.cpp:1476` and `:1569`.

So `wslkit limit` would be wslkit writing limits WSL itself never writes. That
is possible where `wsl-user` exists, because root in a distribution is root in
the VM and the controllers are enabled. It is not possible at all before that.

## Nothing on the Windows side reports it

Checked, because a host-side source would have avoided all of this:

- One host process covers every distribution. `HostingProcessNameSuffix` is set
  to a constant `"WSL"` (`WslCoreVm.cpp:1571-1574`,
  `src/windows/common/wslutil.h:53`), so `vmmemWSL` is the whole VM.
- `HcsGetComputeSystemProperties` is called once, for the runtime id
  (`src/windows/common/hcs.cpp:150-165`). No memory or CPU property is ever
  queried.
- The service's COM interface (`src/windows/service/inc/wslservice.idl`) has no
  statistics method.
- The guest reads `/proc/meminfo` only to drive its own automatic reclaim
  (`util.cpp:3671-3736`) and never reports totals back to the service.

## What `wslkit top` does

Decided in the guest, in one round trip, so the tool does not have to know the
WSL version:

- If `/proc/1/cgroup` names a `/wsl-user/distro-N` node and that node has a
  readable `memory.current`, read it. That is true attribution, and it can also
  report anonymous memory, which is the part automatic reclaim can never give
  back to Windows.
- Otherwise sum the resident set of the distribution's own processes from
  `/proc`. Real attribution of process memory, but it counts a shared page once
  per process that maps it, and it cannot see page cache or kernel memory.

Either way the report says which method it used and what that method leaves out.
The per-distribution figures do not sum to the VM total under either, and saying
why is the difference between a report someone trusts and one they argue with.

CPU comes from the same place. Under the process method it sums fields 14 to 17
of `/proc/<pid>/stat`: the process's own user and system time **and** what it
reaped from children that have since exited. Leaving the latter out was measured
to undercount a fork-heavy workload eightfold, reporting 13.7% where the true
figure was 110.7%.

## Open

- Which WSL release introduced `wsl-user`. Needs a full clone:
  `git log -S WSL_USER_CGROUP_PATH -- src/linux/init/main.cpp`, then
  `git tag --contains`.
- Whether `top` should report the `wsl-user` VM-wide cap, which is a real limit
  a user can hit and has no other visibility.
- Whether `limit` is worth building for 2.8+ only, given it writes limits WSL
  itself does not, and they vanish when the distribution restarts because the
  cgroup is recreated under a new PID.
