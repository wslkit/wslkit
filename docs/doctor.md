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
wslkit doctor undo <id>            put that one back
```

The journal lives in `%LOCALAPPDATA%\wslkit\undo` and is shared by every
subcommand, so a disk change and a doctor fix appear in the same place.

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
| `--no-redact` | leave usernames and paths in the output |
