# What wslkit will and will not do

A tool that touches WSL can break WSL. These are the rules wslkit holds itself
to, so you can tell at a glance which commands are safe to run without thinking
about it.

## Reading is always safe

`wslkit doctor`, `wslkit disk list`, `wslkit disk info`, `wslkit disk usage`,
`wslkit disk orphans` and `wslkit top` change nothing.

They also do not start anything. Starting a distribution to measure it changes
the thing being measured, and it costs you the seconds and the memory of a VM
boot you did not ask for. Where a measurement genuinely needs the distribution
running, the command says so and offers `--probe`, which is off by default.

The one exception is `wslkit disk trim`, which has to run inside the guest and
therefore starts it. Its plan says so before it does.

## Changing needs to be asked for

No command changes anything unless you name a command that changes things.
There is no `--fix-everything`, and `doctor` will never repair something it
found on its own.

`wslkit doctor fix` prints its plan and stops. It changes nothing until you add
`--apply`. Every applied fix records a rollback that `wslkit doctor undo` can
replay.

The disk commands that change things take `--dry-run`, which prints the exact
steps and exits. A dry run takes no measurements either: being free of side
effects means not starting a distribution to look inside it, not merely not
writing.

## Destroying asks twice

Two commands can lose data, and both stop and ask:

- `wslkit disk orphans --delete` asks once for the whole set, having first
  printed what it found and warned that a disk nothing claims is not
  necessarily a disk nothing needs.
- `wslkit disk trash --purge` asks again, because it is the only place in the
  kit where a distribution is destroyed beyond recovery.

End of input counts as no. A piped command with nothing to answer with has not
consented to anything. `--yes` skips the question when you mean it to.

## Order is the safety property

Where a command does several things, the irreversible one is last, and nothing
that would need undoing is scheduled after it. That is checked rather than
merely intended: a plan that breaks the rule is reported as a bug in wslkit.

`wslkit disk move` is the clearest case. It copies the disk, repoints the
registration, starts the distribution to prove it boots from the new location,
and only then deletes the original. Anything that fails before that point is
put back.

## No administrator, and no asking for one

Nothing relaunches itself as administrator. A command that genuinely needs an
elevated console says so and exits; it does not show you a prompt you cannot
audit, and it does not break the output of a `--json` consumer by restarting
itself mid-run.

Almost nothing needs one. Compacting a disk, moving it, reading every check the
doctor runs: all of it works from an ordinary console.

## Nothing leaves your machine

wslkit makes no network requests unless you ask for one. It reads your
registry, your filesystem and your event log, and it writes to your terminal.

There is exactly one request it can make, and only with `--online`:
`GET https://api.github.com/repos/microsoft/WSL/releases`, to find out which
version of WSL is current instead of trusting the list built into the binary.
It sends no information about your machine — the request has no query, no body
and no identifier beyond a user agent saying it is wslkit — and the answer is
cached for a day in `%LOCALAPPDATA%\wslkit\cache`. Without the flag, nothing
reaches the network at all.

`--report` produces a block you can paste into a bug report, with your username,
your hostname and machine identifiers scrubbed. See
[reporting a bug](report-a-bug.md).
