# How wslkit is built

One Go binary, no cgo, statically linked for `windows/amd64` and
`windows/arm64`. Two third-party modules, both justified in a decision record.

The full design document is
[ARCHITECTURE.md](https://github.com/wslkit/wslkit/blob/main/ARCHITECTURE.md);
this is the shape of it.

## Collect, then decide

Everything that touches Windows happens in one place, and everything that
decides what a fact means is a pure function of the facts.

The collectors read the registry, the filesystem, the event log and WMI, and
produce one value. The checks take that value and return findings. They perform
no I/O at all, which is why they compile and run on Linux, and why a saved
`--json` capture can be replayed through them unchanged.

That separation is enforced rather than intended. The build runs the checks
through a Linux compile, where the Windows packages do not exist, and the linter
denies those imports by path.

Every collected fact carries where it came from and, when it could not be read,
why: absent, denied, unsupported, or failed. A check can then say "I could not
tell" instead of guessing, which is the difference between an honest UNKNOWN and
a wrong FAIL.

## Ports where the work is destructive

The disk commands are structured as ports and adapters: the registry, the
filesystem, the virtual disk API, `wsl.exe` and the clock are each an interface.
The command logic depends only on those.

That is not architecture for its own sake. It is the only way to test a locked
file, a full volume, a distribution that will not boot, and a disk the utility VM
will not release, without arranging any of it for real. The clock in particular
is a port because waiting for a disk is tens of seconds of real time.

## Plan, then apply

Every mutating command produces a plan before it does anything: the steps, in
order, and any warnings. `--dry-run` prints the plan and stops.

Planning is read-only, which is what makes a dry run trustworthy. It is free of
side effects rather than merely free of writes: it does not start a distribution
to look inside it either.

A plan may not schedule a step that registers a rollback after a step past which
no rollback can run. That is checked, and a violation is reported as a bug in
wslkit rather than as user error.

## Data rather than code

The error dictionary, the `.wslconfig` key table and the compatibility matrix
are JSON files embedded in the binary, generated from the WSL source and
refreshed by CI. They are data because they change on Microsoft's schedule, not
ours, and because a table is reviewable in a way a switch statement is not.

The reference pages on this site are generated from those same files and from
the probe and fix registries, so the documentation cannot drift from the tool.

## Testing

Checks are tested against saved machine snapshots rather than against whatever
the build agent looks like. A bug report that includes a snapshot can become a
regression test directly.

The pure layers run on Linux in CI, which is also what enforces the separation.
The Windows-only layers run on a Windows agent. The parts that talk to real
Windows APIs, the sparse copy in particular, are tested against a real
filesystem rather than a fake, because that is where the bugs were.

## Decisions

[The decision records](decisions.md) state what was chosen and what it rules
out. The ones that shape everything else: no cgo and a two-module allow-list,
no self-elevation, the collector and check split, and the snapshot-first testing
that follows from it.
