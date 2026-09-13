# top

`wslkit top` shows what the WSL utility VM is using and which distribution is
responsible. It is the answer to "why is vmmem so large".

It reads only, and it measures nothing that is not already running.

```
wslkit top             memory and CPU per distribution
wslkit top --once      one sample, and no CPU rate
wslkit top --json      integer bytes
wslkit top Ubuntu      only these distributions
```

## What it tells you

```
utility VM: 1.6 GiB of 7.6 GiB in use, 5.9 GiB free, 4 processor(s)
            1.1 GiB page cache, which Windows can reclaim; 141.3 MiB anonymous, which it cannot

DISTRIBUTION  MEMORY     CPU     PROCESSES  INIT
Ubuntu        438.3 MiB  110.7%  56         systemd
skrog-engine  126.5 MiB  0.3%    14         init
```

The first line is what Task Manager shows against `vmmem`. The split underneath
is the useful part: **page cache** is memory the VM is holding that Windows can
take back under pressure, and **anonymous** memory is what it cannot. A VM that
looks enormous but is mostly page cache is not a problem.

CPU is a share of one processor, measured across the interval between two
samples, so 110% means rather more than one core's worth.

## The numbers do not add up, on purpose

Per-distribution memory will not sum to the VM total, and the report says so
rather than quietly fudging it. Page cache and kernel memory belong to the VM
rather than to any distribution.

How a distribution is measured depends on your WSL version, and the report tells
you which was used:

- From about WSL 2.8, each distribution gets its own cgroup, which accounts for
  it alone. That is true attribution, and it can also report anonymous memory
  per distribution.
- Before that, every distribution's processes share the VM's root cgroup, so
  cgroup accounting read from inside one returns VM-wide numbers. wslkit sums
  the resident set of each distribution's own processes instead. That is real
  attribution of process memory, but it counts a shared page once per process
  that maps it, and it cannot see page cache at all.

That difference was measured rather than assumed. Burning five seconds of CPU in
one distribution on the older arrangement moves every other distribution's
counter by the same five seconds. The measurements are in
[the research note](https://github.com/wslkit/wslkit/blob/main/docs/research/2026-09-cgroups.md).

## Flags

| Flag | What it does |
|---|---|
| `--json` | machine-readable output, sizes in bytes |
| `--interval D` | gap between the two samples a CPU rate is measured over, default two seconds |
| `--once` | take one sample and report no CPU, rather than waiting |
| `--timeout D` | bound on each measurement inside a distribution |

A CPU rate needs two samples separated by time. Asked for one with `--once`, the
report omits the column rather than printing a number that means something else.

## Limits

There is no `wslkit limit`. WSL has no per-distribution memory or CPU cap: it
writes one VM-wide limit and never gives a distribution one of its own, so
`.wslconfig` `memory=` and `processors=` remain the only supported controls.
Anything finer would be wslkit writing limits WSL itself does not, and it is
tracked as an open question rather than shipped.
