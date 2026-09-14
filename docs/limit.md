# Capping one distribution

`.wslconfig` caps the whole utility VM. That is the only memory setting WSL
has, so a runaway build in one distribution takes memory from every other one
and from Windows, and there is nothing anywhere that says "this distribution
gets four gigabytes".

From WSL 2.9 there is somewhere to put one.

```
wslkit limit show                                   what everything is capped at
wslkit limit set -d Ubuntu --high 3GB --cpus 2      cap it
wslkit limit clear -d Ubuntu                        remove the caps
```

## Which limit to use

**`--high` is usually the one you want.** Past it the kernel reclaims
aggressively: the distribution gets slower, pages out, and keeps running. It is
a brake.

**`--memory` is a hard ceiling.** Past it the kernel kills a process inside the
distribution — your compiler, your database, whichever it picks — with no
warning to anything outside. It is a wall.

Setting both is the sensible shape: a brake at the figure you expect, a wall
some way above it.

```
wslkit limit set -d Ubuntu --high 3GB --memory 4GB
```

`--cpus` takes a fraction, so `--cpus 1.5` is a processor and a half.
`--swap SIZE` caps swap and `--no-swap` turns it off for that distribution.

A ceiling below what the distribution is using right now is refused rather than
applied, because the kernel would accept it and immediately start killing
things:

```
$ wslkit limit set -d Ubuntu --memory 100MB
/wsl-user/distro-4975 is using 1.1 GiB now, and a ceiling of 100.0 MiB would have
the kernel start killing processes inside it immediately. Raise the limit, or
free some memory first
```

## It works, and here is the proof

Four busy loops inside a distribution capped at `--cpus 1.5`, on a
four-processor VM, over five seconds:

```
cpu used in 5s: 7548 ms of a possible 5000 ms per processor
that is 150% of one processor
nr_throttled 61
throttled_usec 15122230
```

Uncapped that workload takes 400%. It took exactly the 150% asked for.

## The catch

**The limits do not survive a restart of the distribution.** Each distribution's
cgroup is named after its init process — `/sys/fs/cgroup/wsl-user/distro-4975` —
and WSL makes a fresh one every time the distribution starts, which it does
whenever you run a command after it has been idle. Nothing carries the old
settings across.

So either set them again when it matters, or have the distribution do it
itself on every boot, with a line in its `/etc/wsl.conf`:

```ini
[boot]
command = /usr/local/sbin/wslkit-limit
```

where that script writes the same values:

```sh
#!/bin/sh
node=$(awk -F: '{print $3}' /proc/self/cgroup | sed -n 's,^\(/wsl-user/distro-[0-9]*\).*,\1,p')
[ -n "$node" ] || exit 0
echo 3221225472 > "/sys/fs/cgroup$node/memory.high"
echo 200000 100000 > "/sys/fs/cgroup$node/cpu.max"
```

`wslkit limit set --dry-run` prints exactly the values to put in it.

## Which WSL you need

`wslkit limit show` says plainly when the runtime is too old:

```
limit: Ubuntu has no cgroup of its own (/sys/fs/cgroup/wsl-user/distro-N). That
arrived between WSL 2.7.13 and 2.9.11; on an older runtime there is nowhere to
put a per-distribution limit, and .wslconfig caps the whole VM instead
```

Measured, not guessed: on 2.7.13 there is no `wsl-user` at all and every
distribution reads the same VM-wide counters. On 2.9.11 each has its own node
and its own accounting. There is no 2.8 — Microsoft publishes 2.7.x as stable
and 2.9.x as pre-release, with nothing in between — so the change landed
somewhere in that gap. See `docs/research/2026-09-cgroups.md` for both sets of
measurements.

`wsl --update --pre-release` gets you there today.
