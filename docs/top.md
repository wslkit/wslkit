# top

`wslkit top` shows what the WSL utility VM is using and what inside it is
responsible. It is the answer to "why is vmmem so large", and the `docker stats`
WSL does not have: one row for everything using the VM, with the same columns
for each.

It reads only, and it measures nothing that is not already running.

```
wslkit top             memory, CPU, disk and pressure, refreshed until Ctrl+C
wslkit top --once      one report, then exit
wslkit top --json      one report as JSON, integer bytes
wslkit top --watch     keep refreshing even when piped
wslkit top --wsl       only the WSL section: the utility VM and its distributions
wslkit top --wslc      only the wslc section: running wslc session VMs
wslkit top Ubuntu      only these distributions
```

## What it tells you

```
── utility VM ────────────────────────────────────────────────────────────────
1.2 GiB of 7.6 GiB in use, 6.3 GiB free, 4 CPUs, CPU 22.0%
616.5 MiB page cache (reclaimable), 179.3 MiB anonymous (unreclaimable)
stalled over the last 10 s: memory 0.0%, I/O 15.2%, CPU 3.4%
network: 691 B/s in, 696 B/s out

NAME          KIND    MEMORY     ANON       CPU    READ       WRITE      PIDS  STALL MEM/IO  INIT
Ubuntu        distro  469.6 MiB  105.4 MiB  15.3%  1.6 KiB/s  1.6 KiB/s  88    0.0% / 6.8%   systemd
skrog-engine  distro  199.2 MiB  66.6 MiB   6.2%   0 B/s      0 B/s      90    0.0% / 4.5%   init(skrog-engi
docker        cgroup  16.7 MiB   4.9 MiB    0.0%   0 B/s      0 B/s      6     0.0% / 0.0%
WSL itself    wsl     696.0 KiB  116.0 KiB  0.6%   0 B/s      0 B/s      1     0.0% / 0.0%
```

The first line is what Task Manager shows against `vmmem`. The split underneath
is the useful part: **page cache** is memory the VM is holding that Windows can
take back under pressure, and **anonymous** memory is what it cannot. A VM that
looks enormous but is mostly page cache is not a problem.

### Choosing sections

By default top shows both sections: the utility VM with its distributions, and
the wslc session VMs. `--wsl` shows only the first and `--wslc` only the second;
both flags together are the same as neither. A section that is not shown is not
measured either: `--wslc` alone runs nothing in a distribution, and `--wsl` alone
never calls wslc.

The wslc section appears by default when a session VM is running, or when wslc is
installed and none is, in which case it says so. On a machine without wslc there
is none. `--wslc` always shows it, empty or not.

### What Windows charges

```
Windows charges it 681.5 MiB (vmmem pid 23688)
1 other VM(s) hold 696.0 MiB more: a wslc session, or any other Hyper-V VM
```

Every Hyper-V VM on the machine has a `vmmem` process, and its working set is
what Windows is holding for that VM. The gap between it and the kernel's own
figure is memory the VM has let go of but Windows has not reclaimed yet.

top finds the utility VM's `vmmem` by when it started: a `vmmem` is created
when its VM boots, and the guest reports how long ago that was. Nothing else
identifies one without elevation. Its owner is the VM's own account, which a
normal user cannot read, and `hcsdiag` needs Hyper-V administrator rights. Two
VMs booted within three seconds of each other cannot be told apart, and then
neither is claimed.

Every other `vmmem` is reported as another VM, without a name. A wslc session
runs its containers in a VM of its own, and that is usually what it is, but
Windows Sandbox or any Hyper-V guest looks the same. Those VMs are reported
even when no distribution is running.

### wslc containers

```
── wslc session wslc-cli-user (preview) ──────────────────────────────────────
548.4 MiB of 7.6 GiB in use, 7.0 GiB free, 4 CPUs, CPU 103.2%
238.3 MiB page cache (reclaimable), 55.4 MiB anonymous (unreclaimable)
stalled over the last 10 s: memory 0.0%, I/O 2.5%, CPU 0.6%
network: 24 B/s in, 24 B/s out
Windows charges it 786.0 MiB (vmmem pid 39264)

NAME           KIND  MEMORY     ANON       CPU    READ   WRITE  PIDS  STALL MEM/IO
wk-spike-busy  wslc  4.8 MiB    136.0 KiB  99.9%  0 B/s  0 B/s  1     0.0% / 0.1%
wk-spike-idle  wslc  724.0 KiB  132.0 KiB  0.0%   0 B/s  0 B/s  1     0.0% / 0.0%
```

wslc, the container CLI in the WSL 2.9 pre-releases, runs each session's
containers in a VM of its own. For each session whose VM is running, top adds
a section: that VM's totals, what Windows charges for it, and one row per
running container, with the same columns as everything else.

A stopped session is not asked anything: **asking wslc
about a session's containers starts that session's VM** if it was stopped. top
tells the two apart without asking wslc. A running VM has its disk attached, and
Windows' Restart Manager reports the session's `storage.vhdx` in use; a stopped
one's disk is held by nobody. That query holds no handle on the file, so it cannot
get in the way of a VM that is starting.

The numbers come from the containers' own cgroups inside the session VM, read
through `wslc system session run`, not from `wslc stats`. Those print display
strings (`988KiB / 7.611GiB`) rather than numbers. wslc is a preview, and its
output has changed in every 2.9 release so far. See
[the research note](https://github.com/wslkit/wslkit/blob/main/docs/research/2026-09-wslc-session.md).

None of this is visible from `top` or `free` inside a distribution. They see
their own processes. They do not see the other distributions, WSL's own
processes, or the VM's page cache, and they cannot tell you whether anything is
waiting.

### The columns

| Column | What it is |
|---|---|
| `MEMORY` | everything charged to the row, including the page cache its reads and writes pulled in |
| `ANON` | the part of that Windows can never reclaim |
| `SWAP` | swapped-out memory; shown only when something has swapped |
| `CPU` | a share of one processor, so 110% means rather more than one core's worth |
| `READ`, `WRITE` | disk bytes per second |
| `PIDS` | processes and threads, as `docker stats` counts them |
| `STALL MEM/IO` | pressure: the share of the last ten seconds in which something in the row was stalled waiting for memory or for disk |

Usage says a resource is used. Pressure says it is short. A distribution at
80% of the VM's memory with no memory stall is fine. One at 30% with a
persistent stall is the thing to look at.

The kernel's OOM kills are counted too, and a row that has had any gets a
note. Each VM is a section of its own under a rule naming it. A notes section
follows only when this run found something to say: a row that could not be
measured, OOM kills, a VM that could not be read. On a healthy machine there is
none. What explains the numbers in general, everything on this page in short,
is in `wslkit top help`.

### The rows that are not distributions

- **`WSL itself`** is WSL's own processes inside the VM (`wsl-user/non-distro`), which belong to no distribution. Its KIND is `wsl`, and in `--json` it is the group named `wsl`.
- **A `cgroup` row** is a cgroup at the VM's root that belongs to no
  distribution. `/docker` is where Docker Engine without systemd puts its
  containers, and those containers are **not** inside the distribution that
  runs dockerd. Without this row that memory would be in no row at all. For
  one row per container, use the engine's own tooling.

Docker Engine using the systemd cgroup driver should put its containers under
the distribution's own systemd slice instead, so they would count towards the
distribution. That follows from how the driver works and has not been measured
here.

### Network

Network traffic is for the whole VM only. All distributions share one network
namespace: on WSL 2.9.12 two distributions report the same namespace and the
same `eth0` counters. So there is no honest per-distribution number, and top
does not print one. It counts the `eth` interfaces only, because `docker0`,
bridges and `veth` pairs carry the same packets again on their way out.

## The numbers do not add up, on purpose

Per-distribution memory will not sum to the VM total, and top does not quietly
fudge it so that it does. Page cache and kernel memory belong to the VM
rather than to any distribution.

How a distribution is measured depends on your WSL version. `--json` says which
was used, in its `method` and `note` fields:

- On WSL 2.9, each distribution gets its own cgroup, which accounts for it
  alone. That is true attribution, and it carries everything in the table.
- Before that, on WSL 2.7, every distribution's processes share the VM's root
  cgroup, so cgroup accounting read from inside one returns VM-wide numbers.
  wslkit sums the resident set of each distribution's own processes instead.
  That is real attribution of process memory, but it counts a shared page once
  per process that maps it, and it cannot see page cache at all. Disk I/O,
  PIDs, pressure and the extra rows need the per-distribution cgroup, so on 2.7
  they are left out rather than approximated.
  VM-wide pressure is shown on both.

At the time of writing WSL 2.9 is a pre-release and 2.7 is the current stable
release. top decides which method to use by looking for the per-distribution
cgroup, not by reading the version number.

That difference was measured rather than assumed. Burning five seconds of CPU in
one distribution on the older arrangement moves every other distribution's
counter by the same five seconds. The measurements are in
[the research note](https://github.com/wslkit/wslkit/blob/main/docs/research/2026-09-cgroups.md).

## Rates

A rate needs two samples separated by time. Asked for a single instant sample,
with `--interval 0`, the report omits the rate columns rather than printing a
number that means something else.

Rates divide by the VM's own clock between the two samples, not by `--interval`.
Measuring every distribution takes time too, about 190 ms a sweep on the machine
this was written on. Dividing by the interval alone made a two-second CPU rate
read about 9% high.

## Refreshing, or one report

On a console top refreshes, as `top`, `htop` and `docker stats` do. Anywhere
else, a pipe, a file, or `--json`, it prints one report and exits, so a script
never waits on a command that does not end. `--once` prints one report on a
console too, and `--watch` keeps refreshing when piped.

Refreshing measures once per interval and compares each sample with the one
before it. On a console it draws on the alternate screen, as `top` and `htop`
do: one frame in place, and the screen you had comes back when you press
Ctrl+C. A frame taller than the window is cut to fit, with a line saying how
much is hidden, so the header never scrolls away. Piped or redirected, it writes one
report after another, or with `--json` one object per line.

## Flags

| Flag | What it does |
|---|---|
| `--json` | machine-readable output, sizes in bytes |
| `--interval D` | gap between the samples a rate is measured over, default two seconds; `0` is one instant sample with no rates |
| `--once` | print one report and exit, instead of refreshing on a console |
| `--watch` | keep refreshing every interval even when piped or redirected; with `--json`, one object per line |
| `--wsl` | show the WSL section: the utility VM and its distributions |
| `--wslc` | show the wslc section: the wslc session VMs that are running. Never starts one; with none running, the section is shown empty and says so |
| `--raw` | print what the measurement printed inside each distribution, unparsed, labelled with the WSL version |

`--raw` is for when the numbers look wrong. Its output is exactly what top's
parser reads, so a copy of it in a bug report can become a test fixture: the
2.7 tests were made from it.
| `--timeout D` | bound on each measurement inside a distribution |

## Limits

top shows what is used. To cap one distribution, see [limit](limit.md).
`.wslconfig` `memory=` and `processors=` remain the VM-wide controls.
