# WSL will not start

A distribution that refuses to launch usually prints an error code and nothing
else useful. This is the order that gets from that to a cause fastest.

## Read the error

```
wslkit doctor explain "Error code: Wsl/Service/E_UNEXPECTED"
```

Paste whatever `wsl.exe` printed. It takes the full line, a bare `0x80370102`,
a code name on its own, or the exit code.

The middle part of the code is the context, and it narrows the problem more than
the code does. `Wsl/Service/...` failed in the service; `Wsl/InstallDistro/...`
failed while installing one.

`explain` then runs the checks that bear on that error, so you get the meaning
and the state of your machine together.

## Then check the machine

```
wslkit doctor
```

Findings are ranked, worst first. The common causes it separates:

**The optional features are not on.** Virtual Machine Platform in particular.
This is the single most common cause on a fresh machine, and the fix is a reboot
after enabling it.

**Virtualisation is off in firmware.** Nothing in Windows can fix that.

**A third-party hypervisor has it.** VirtualBox and VMware historically took
exclusive control. The check names what it found.

**The runtime is too old for the distribution.** This is the one that produces
the most confusing failures, because the distribution installs and then will not
boot. Ubuntu 26.04 is cgroup-v2-only and needs a WSL that stopped mounting
cgroup v1, which arrived in 2.5.1 and was not reliably fixed until 2.5.7. See
[distribution compatibility](compatibility.md).

**The disk is corrupt or missing.** `wslkit disk list` shows whether the file is
where the registry says it is, and `wslkit disk orphans` finds it if it moved.

## Facts it could not read

Some checks report UNKNOWN rather than guessing. They need an administrator to
read what they look at.

```
wslkit doctor --elevated
```

from a console that already has one. wslkit will not relaunch itself to get it.

## Then fix

```
wslkit doctor fix <id>            print the plan
wslkit doctor fix <id> --apply    make the change
wslkit doctor undo                put it back
```

Nothing is applied on your behalf. Each finding that has a fix names it.

## If it still will not start

Save the machine and take it with you:

```
wslkit doctor --json > machine.json
```

That file can be replayed with `--from-snapshot` on any machine, which is how a
bug report becomes reproducible. See [reporting a bug](report-a-bug.md).
