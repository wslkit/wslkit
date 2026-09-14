# doctor

`wslkit doctor` works out why WSL is broken or slow, ranks what it found, and
can fix some of it.

It reads only. No administrator, no configuration touched, no distribution
started. Running it is always safe.

```
wslkit doctor                              every check, ranked
wslkit doctor explain "Wsl/Service/E_UNEXPECTED"
wslkit doctor fix <id>                     the plan, having changed nothing
wslkit doctor fix <id> --apply             make the change
wslkit doctor undo                         what can be replayed
```

Bare `wslkit doctor` runs `check`, the way `brew doctor` does.

## check

Runs every check against this machine and prints the findings worst first.

A check reports one of four things. **FAIL** is something wrong that explains a
symptom. **WARN** is something that will bite later. **UNKNOWN** is a fact it
could not establish, usually because it needs an elevated console. **OK** is
fine and hidden unless you ask for it.

```
wslkit doctor --verbose          show the checks that passed too
wslkit doctor --only M1          only checks in one milestone
wslkit doctor --elevated         re-read the facts that need an administrator
wslkit doctor --json             machine-readable, schema wslkit/result/v1
```

A check whose dependency failed is reported as skipped rather than guessed at.
One broken fact should not produce a page of misleading findings.

[Every check](probes.md) is listed in the reference, generated from the same
registry the binary runs.

### Working on someone else's machine

`--json` output is also an input. Save it, and every check can be re-run
against that saved machine on yours:

```
wslkit doctor --json > machine.json
wslkit doctor --from-snapshot machine.json
```

That is how a bug report becomes something reproducible. It is also how the
checks are tested: the test suite runs them against saved snapshots rather than
against whatever the build agent happens to look like.

## preflight

Reads a `.wsl` distribution file and says what installing it would run into,
before you install it.

```
wslkit doctor preflight Ubuntu-26.04.wsl
```

A `.wsl` file is a tar archive holding a whole root filesystem plus a few small
configuration files that decide how WSL sets it up. `wsl --install --from-file`
unpacks several gigabytes and only then reports a mistake in one of those
200-byte files, as an HRESULT. And a distribution that installs perfectly can
still fail to start on the runtime you have.

All of that is readable from the archive's headers beforehand. Nothing is
extracted, nothing is written, nothing is installed; a 400 MiB archive takes
about a second.

```
Ubuntu-26.04.wsl: gzip archive, 24184 entries, 1.2 GiB

OK      PRE001  wsl-distribution.conf is valid (5 settings)
OK      PRE002  Default user is uid 1000 (ubuntu), which exists in /etc/passwd
OK      PRE003  wsl.conf is valid (2 settings)
WARN    PRE004  1 enabled unit(s) known to misbehave under WSL: systemd-resolved.service
FAIL    PRE006  systemd 258 needs WSL 2.5.7 or newer; this machine has 2.4.13
```

| Check | What it looks at |
|---|---|
| `PRE001` | `/etc/wsl-distribution.conf`: only the keys WSL's init actually reads, and their values |
| `PRE002` | the default uid exists in `/etc/passwd`, or a first-run command creates it; uid 0 is `root` |
| `PRE003` | the `/etc/wsl.conf` the archive ships, against the same key table the live check uses |
| `PRE004` | enabled systemd units known to misbehave under WSL |
| `PRE005` | extended attributes a WSL 1 install would silently drop |
| `PRE006` | whether **this machine's** runtime can install and start it |

The last one is the one no distribution validator can make for you. The case
that catches people: a distribution whose systemd is cgroup v2 only installs
fine on an older runtime and then never boots, with an error that says nothing
about cgroups.

Exit codes follow the usual contract: `0` clean, `1` warnings, `3` something
that would stop the install. So a build script can gate on it:

```
wslkit doctor preflight dist.wsl || exit 1
wsl --install --from-file dist.wsl
```

xz- and zstd-compressed archives are named as unreadable rather than failing
with a confusing tar error. Unpack them first and check the tar.

## explain

Takes an error WSL printed and tells you what it means and which checks bear on
it.

```
wslkit doctor explain "Error code: Wsl/Service/E_UNEXPECTED"
wslkit doctor explain 0x80370102
wslkit doctor explain 4294967295
```

It accepts whatever form you have: the full line `wsl.exe` printed, a bare
HRESULT, a code name on its own, or the exit code. The middle part of
`Wsl/Service/E_UNEXPECTED` is the context, which usually narrows the problem
more than the code does.

Then it runs the checks that relate to that error, so you get the meaning and
the state of your machine in one go.

[Every code it knows](errors.md) is in the reference.

## fix

Applies one remediation, named explicitly.

```
wslkit doctor fix wslconfig            print the plan
wslkit doctor fix wslconfig --apply    do it
```

Printing the plan is the default. Nothing changes until `--apply`, and what
changes is recorded so `undo` can put it back.

A fix that needs an administrator says so and exits if the console does not
already have one. wslkit does not relaunch itself; see the decision record on
elevation.

Most fixes can be rolled back. One cannot: `zone` deletes the
[`:Zone.Identifier` files](zone-identifier-files.md) a saved download leaves
inside a distribution, and there is nothing to restore them from. It says so in
the plan, before you apply it.

[Every fix](fixes.md) is in the reference.

## undo

Lists what has been applied, and replays a rollback.

```
wslkit doctor undo                 the journal
wslkit doctor undo <id> --dry-run  the rollback steps, changing nothing
wslkit doctor undo <id>            put that one back, after one confirmation
```

The journal lives in `%LOCALAPPDATA%\wslkit\undo` and is shared by every
subcommand, so a disk change and a doctor fix appear in the same place.

`undo` is the one command whose whole purpose is to change the machine back, so
it prints what it would do and asks before doing it. `-y` skips the prompt for
a script; `--dry-run` is the safe way to see what an entry from three weeks ago
would actually undo.

## Flags

| Flag | What it does |
|---|---|
| `--json` | machine-readable output, schema `wslkit/result/v1` |
| `--report` | a redacted block to paste into a bug report |
| `--verbose` | show OK and skipped findings too |
| `--only M1[,M2]` | run only checks tagged with these milestones |
| `--from-snapshot FILE` | run against a saved machine instead of this one |
| `--elevated` | require an elevated console and re-read admin-only facts |
| `--allow-vm-wake` | permit checks that would start the WSL VM |
| `--timeout DURATION` | per-collector deadline, default five seconds |
| `--online` | check the published WSL releases instead of the built-in list |
| `--no-redact` | leave usernames and paths in the output |
