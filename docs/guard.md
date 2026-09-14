# WSL hangs after the laptop wakes up

You close the lid. Later you open it, type something into a shell, and nothing
happens. Not an error — nothing. `wsl --shutdown` hangs too, and the only thing
that reliably works is restarting Windows.

## Why

WSL handles no power events at all. Its service registers for none of them, and
the Linux init has no resume hook, so nothing anywhere in the product knows the
machine slept. The AF_HYPERV sockets that carry every command into the VM are
torn down entering Modern Standby and never rebuilt, and the next command waits
for an answer from a socket that no longer exists.

This has been reported for years
([#8696](https://github.com/microsoft/WSL/issues/8696),
[#12747](https://github.com/microsoft/WSL/issues/12747),
[#14005](https://github.com/microsoft/WSL/issues/14005)) and there is no fix and
no maintainer statement. What there is, in those threads, is a recovery
sequence people have worked out for themselves. That is what this automates.

## Use it

```
wslkit guard install               run it on resume and at logon
wslkit guard install --elevated    and permit the two steps that need admin
wslkit guard status                what is installed, and what the last run did
wslkit guard uninstall             remove it
```

Or, when it has already happened and you just want your shell back:

```
wslkit guard run-once
```

## What it does

It waits fifteen seconds for the machine to settle, then probes: `wsl --status`
with a ten-second deadline, and a command that does nothing inside the default
distribution with a twenty-second one. A refusal and a silence are different
findings — a runtime that answers "not installed" is not one that has stopped
answering — and only silence is worth acting on.

If something is genuinely wedged, it climbs a ladder, re-probing after each step
and stopping at the one that worked:

| Step | Needs admin | Why it is at this height |
|---|---|---|
| `wsl --shutdown` | no | works whenever the service still answers |
| `wsl --shutdown --force` | no | terminates the compute system directly, so it does not need the socket that is broken |
| kill `wslservice.exe` | yes | the most-reported working step upstream |
| restart `WSLService` | yes | reports of it working alone are mixed, so it is last |

**It never touches `vmcompute` or `HvHost`.** Stopping vmcompute has been
reported to bluescreen the machine, and no hung distribution is worth that.

Without `--elevated` the last two steps are not attempted at all, rather than
attempted and failed: a log full of access-denied lines teaches you to ignore
the log.

## What it will not do

**Recover a distribution that was not running before the machine slept.** That
one is stopped, not broken, and starting a distribution from cold takes long
enough to look exactly like a hang. Shutting WSL down every morning because a
cold start was slow is how a tool like this gets uninstalled.

Knowing which is which needs a record, so every run writes down what was
running. The first run on a machine has no record and therefore does nothing;
from then on it has one, at most one sleep old.

**Restart Windows.** If the whole ladder fails, it says so and stops.

## The clock

A guest whose clock is minutes out fails TLS handshakes, and it is a common
after-effect of a long sleep. chronyd inside the distribution steps the clock
for its first three corrections and slews after that, so an offset built up over
a night can take most of a morning to close on its own.

When the guest is more than five seconds out, `guard` steps it from the
hardware clock, which the hypervisor keeps right.

## The log

Everything goes to `%LOCALAPPDATA%\wslkit\guard.log`, one line per decision:

```
2026-09-14 08:31:07  Ubuntu was running before the machine slept and does not answer now
2026-09-14 08:31:07  trying: wsl --shutdown
2026-09-14 08:31:22  WSL answers again after wsl --shutdown
```

An unattended tool that recovers your machine and leaves no account of what it
did is one you cannot trust the next time it does something surprising.
`guard status` shows the last few lines.
