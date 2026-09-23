# What wslkit can read from a wslc session, and what starts one

Spike for #96, on WSL 2.9.12 (pre-release), Windows 10 19045, 2026-09-22.
Everything here was run as a normal user without elevation, which is how
`wslkit top` runs.

## The session VM is a second VM

A wslc session runs in its own VM, separate from the WSL utility VM, with its
own `vmmem` process. With two idle busybox containers it held **750 MiB** of
working set. `wslkit top` measured only the utility VM, so none of that was
visible.

Inside, the session VM is a system image (`/bin` has `WSLGd`, `Xwayland`), not
a distribution. It has `sh`, `awk`, `sed` and `getconf`, `/proc/pressure/*`, an
`eth0`, and one cgroup at the root: `/docker`, with one child per container,
named by the full container ID. There is no `wsl-user`.

## Which wslc commands start the VM

Starting state: a session listed (`wslc-cli-user`), `wslcsession.exe`
running, and one `vmmem`, the utility VM's. The session VM had stopped by
itself earlier. Each command was run once, and the `vmmem` count checked
after it:

| Command | Started the session VM? |
|---|---|
| `wslc info --format json` | no |
| `wslc system session list` | no |
| `wslc list --all --format json` | **yes**: a second `vmmem` appeared, and the guest's uptime was 43 s shortly after |

So `wslc list`, and presumably `stats` too, is not a read-only question: it
boots the VM to answer it. `top` must not call either unless the session VM is
already up.

`info` and `session list` print the same thing whether the session VM is up or
not, so neither can say which it is. `wslcsession.exe` runs in both states, so
it is no signal either. Not measured: whether `info` or `session list` start
`wslcsession.exe` from a state where it is not running. It was already running
at the start.

## Reading the session VM

`wslc system session run <command>` runs as root inside the session VM and
forwards standard input. `top`'s existing `SampleScript` ran there unchanged
and exited 0. It took the process method, correctly, since there is no
`wsl-user`, and reported the VM's memory, CPU, pressure and network.

What it did not report is the containers. Each one is a cgroup at
`/sys/fs/cgroup/docker/<full id>` with the same files as a distribution's
cgroup, so the `group()` function in the script can read them as they are.

## `wslc stats --format json`

One object per line, one line per container:

```
{"BlockIO":"69.6kB / 0B","CPUPerc":"100.54%","ID":"2e0471...","MemPerc":"0.01%","MemUsage":"988KiB / 7.611GiB","Name":"wk-spike-busy","NetIO":"932B / 0B","PIDs":1}
```

The values are `docker stats` display strings, not numbers. They mix decimal
(`kB`) and binary (`KiB`) units, and a percentage is already rounded. Parsing
them back is lossy, and the format has changed in every 2.9 release so far.
Reading the cgroups through `session run` gives integers and the same columns
as every other row, so that is the better source. `wslc list --format json` is
still needed once, for the container ID → name mapping. It uses the same
one-object-per-line form, with `ID` truncated to 12 characters and `Names`
holding the name.

## Matching a vmmem to its VM, without admin

- `hcsdiag list` refuses: "Only administrators or users that are members of
  the Hyper-V Administrators user group are permitted".
- `Win32_Process.GetOwner` on `vmmem` returns 2 (access denied). The owner
  would have been the VM's virtual account.
- `vmwp.exe`'s command line is not readable.
- **`Win32_Process.CreationDate` is readable, and it matches.** A `vmmem`'s
  creation time equals its VM's boot time, which the guest reports through
  `/proc/uptime`:

| VM | `vmmem` created | guest boot (now − uptime) |
|---|---|---|
| utility VM | 15:51:21.70 | 15:51:22.24 |
| wslc session VM | 17:26:47 | 17:26:48.18 |

  Both agree to within about a second. Two VMs booted within the same second
  would be ambiguous; nothing else is.

## What this means for #97 and #98

- **#98 is feasible without admin.** Match the utility VM's `vmmem` by
  creation time against the uptime `top` already samples. Report its working
  set beside the kernel's figure. Report every other `vmmem` as another VM.
- **#97 has one unsolved problem: knowing the session VM is up before asking
  wslc anything.** The only non-admin signal found is indirect: a `vmmem`
  that is not the utility VM's, while a wslc session is listed. Any other
  Hyper-V VM also has a `vmmem` (Windows Sandbox, a Hyper-V guest, another
  engine's VM). With one of those running and the wslc session idle, the
  heuristic says "up", and the first `wslc` call boots the session VM. That
  breaks top's rule.

## Resolution

#97 shipped as an opt-in `--wslc`, so booting the session VM only happens when
someone asks for it. The flag was removed before any release, from `top` and
`doctor` alike, once a way to tell a running session VM from a stopped one
without asking wslc was found (see "Telling a running session VM from a
stopped one" below): both now read only sessions whose VM is already up.
`top` later gained `--wsl` and `--wslc` again, but only to choose which
sections to show; neither starts anything. Observed while building it:

- **`wslc system session run` boots a stopped session VM too.** `top --wslc`
  run against a stopped VM went from one `vmmem` to two, and its only calls
  were `wslc info` (which does not boot the VM) and `session run`.
- **With no containers, the session VM stopped by itself after about 40
  seconds.** Twice, both times after its containers were removed.
- **Right after boot there is no `/docker` cgroup** until a container runs.
  That is a session with no containers, not an error.
- **Whether top started the VM is decided by comparing the matched `vmmem`'s
  creation time with the sweep's start.** Both are on the Windows clock. The
  guest's clock could not decide it: the VM came up within a second of the
  sweep starting. Verified both ways: the note appears on a cold run and not
  on a warm one.

## Left behind

Nothing. The two test containers (`wk-spike-idle`, `wk-spike-busy`) were
removed. The pre-existing `skrog-share-c` container and the session were not
touched. The session VM was started by the `wslc list` test and left to stop
on its own idle timeout, as it had before.

## Containers cannot resolve names (2026-09-23)

On this machine (Windows 10 19045, WSL 2.9.12), `apt update` failed in every
wslc container, while the distributions, and Docker inside skrog-engine, were
fine.

- **The session VM's network is WSL's user-mode device host, not NAT and not
  mirrored.** `.wslconfig` sets no `networkingMode`. The VM has the host's own
  address, and a container's connection to `1.1.1.1:80` appeared on Windows as
  a socket owned by `dllhost.exe` with `wsldevicehost.dll` loaded, registered
  as the `WslDeviceHost` COM class. The host re-creates the VM's connections
  as its own sockets.
- **A container is given only the host's IPv4 resolver.** The session VM's
  resolv.conf had the host's IPv4 resolver and its IPv6 ones. Containers
  without IPv6, the default, get only the IPv4 one.
- **Queries to that resolver fail inside the PC.** From the session VM, the
  host's resolver answered SERVFAIL, over UDP and TCP, in 0 ms. A packet
  capture on every Windows network component showed the Windows host's own
  query reaching the resolver and being answered, and the container's and the
  session VM's queries appearing nowhere. Nothing in the session VM redirects
  port 53 or listens on it.
- **Queries to any other resolver work.** `--dns 1.1.1.1` fixed `apt update`.
- **Ruled out:** the router (it answers the same address from Windows), and
  `dnsTunneling=true` in `.wslconfig` (the failure was the same with it
  commented out and the session VM freshly booted).

Not established: whether wslc DNS worked on this machine before the 2.7 round
trip, and which part of the device host produces the SERVFAIL.

`wslkit doctor` checks for this (`WSC001`), for sessions whose VM is running.

## Telling a running session VM from a stopped one (2026-09-23)

Asking wslc anything boots a stopped session VM, so a check has to know first.
Measured on WSL 2.9.12, across several boots and idle stops, without elevation:

| signal | running | stopped | usable |
|---|---|---|---|
| Restart Manager: who holds `sessions\<name>\storage.vhdx` | `System` | nobody | **yes**, per session, and it holds no handle |
| `sessions\<name>\swap.vhdx` exists | yes | no | as corroboration; a crash could leave it behind |
| exclusive open of `storage.vhdx` | sharing violation | opens | no: the handle could stop a VM booting at that instant |
| `swap.vhdx` creation time against the VM's `vmmem` | | | no: NTFS tunneling hands a re-created `swap.vhdx` its predecessor's creation time |
| the `WslDeviceHost` `dllhost.exe` | present | **present** | no: it outlives the VM |
| an extra `vmmem` | present | absent | no: any Hyper-V VM has one |

Restart Manager answers the same way for a distribution's disk: a running
skrog-engine's `ext4.vhdx` was held by `System` and `WSL Service`.
