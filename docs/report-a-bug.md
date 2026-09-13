# Report a bug well

Most WSL bug reports are an error code and a version number, and most of the
back and forth afterwards is someone asking what the machine looked like. wslkit
can answer that in one paste.

## For a wslkit bug

```
wslkit doctor --report
```

Prints a markdown block for an issue: what wslkit found, the versions involved,
and the state that produced it. Usernames, hostnames and machine identifiers are
scrubbed.

Include the command you ran and what you expected instead. If a command failed,
its exit code narrows it a long way; see [exit codes](exit-codes.md).

## For a WSL bug

Microsoft's own log collector is the right tool for a report to them, because
their triage is built around it. wslkit is the faster way to find out whether
you have a WSL bug at all:

```
wslkit doctor
wslkit doctor explain "<the error wsl.exe printed>"
```

If a check explains it, you have a configuration problem rather than a bug, and
the finding says what to do.

## Making it reproducible

The most useful thing you can attach is the machine itself:

```
wslkit doctor --json > machine.json
```

Every check can be re-run against that file on someone else's machine:

```
wslkit doctor --from-snapshot machine.json
```

That turns "it fails on my machine" into something a maintainer can step
through. It is also how wslkit's own checks are tested, so a snapshot that
reproduces a bug can become a regression test directly.

## What is in a snapshot

Registry values under the WSL keys, file and volume facts about your disks, the
versions of the WSL components, which optional Windows features are on, service
states, and the WSL entries from your event log.

No file contents, no network requests, nothing from inside a distribution unless
you asked for it with `--probe`.

Redaction is on by default: your Windows username becomes `%USERPROFILE%`, your
hostname becomes `<host>`, and account identifiers are truncated. `--no-redact`
turns it off if you need the real values for your own debugging, which is worth
thinking about before you paste the result anywhere.

## Where to file

Against wslkit: <https://github.com/wslkit/wslkit/issues>.

Against WSL itself: <https://github.com/microsoft/WSL/issues>, with their log
collector output. If `wslkit doctor explain` matched your error to a known
upstream issue, it prints the link, and adding to an existing thread is usually
more useful than opening another.
