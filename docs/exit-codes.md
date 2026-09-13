# Exit codes

Every wslkit command exits with a number that says what happened, so a script
can branch without parsing prose.

| Code | Means |
|---|---|
| `0` | success, and nothing worth reporting |
| `1` | findings, or a failure that fits no other category |
| `2` | the command line was wrong, or two arguments contradicted each other |
| `3` | refused before anything ran, so nothing changed |
| `5` | some of it worked and some did not |
| `10` | no distribution of that name |
| `11` | the distribution is running, or its disk is held open |

## The ones worth branching on

**`0` and `1` are both normal for `doctor`.** One means it found nothing, the
other means it found something. A script that treats `1` as a crash will treat
every useful run as a failure.

**`3` means nothing changed.** A precondition declined before the command
started: the distribution is WSL 1, the disk is not where the registry says it
is, the destination already holds a file. Retrying without changing something
will do the same thing.

**`11` means try again later.** The utility VM is holding a disk, or a
distribution is still running. This is the one that is worth a retry, and it is
why it has a number of its own rather than being folded into `1`.

**`5` means look at the output.** Some files were deleted and some were not, for
example. The per-item results say which.

## Under `--json`

Errors go to standard output as an object, in the same stream as everything
else, rather than to standard error where a consumer reading the stream would
miss them:

```json
{"error":"distro-not-found","exit_code":10,"message":"disk: no such distribution: \"Nope\" (known: Ubuntu, skrog-engine)"}
```

The `error` field is a stable token, so branching on it does not mean matching a
number or reading English.

| Token | Code |
|---|---|
| `generic` | 1 |
| `usage` | 2 |
| `preflight` | 3 |
| `partial` | 5 |
| `distro-not-found` | 10 |
| `distro-busy` | 11 |

## Compatibility with wsldisk

These are the numbers the archived `wsldisk` published, so a script written
against it keeps working once the command name changes. See
[the parity checklist](https://github.com/wslkit/wslkit/blob/main/docs/wsldisk-parity.md).
