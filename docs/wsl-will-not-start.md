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

**The filesystem inside the disk is damaged.** The `DSK004` check reads the
guest's own ext4 superblock straight out of the VHDX: no VM is started, and a
stopped distribution answers as readily as a running one. The kernel writes
every error it hits into that superblock and only `fsck` clears them, so the
record of the corruption that stopped a distribution from booting is still in
the file afterwards. A distribution that dies with `Wsl/Service/E_UNEXPECTED`
and shows `EXT4-fs error` in `dmesg`, or whose `/sbin/init` cannot load a
shared library, is the shape of it
([microsoft/WSL#13484](https://github.com/microsoft/WSL/issues/13484)). WSL
2.6.0 started reporting a corrupt disk properly when the mount fails, so a
recent runtime may name it for you; an older one only says `E_UNEXPECTED`.

## Facts it could not read

Some checks report UNKNOWN rather than guessing. They need an administrator to
read what they look at, so run it again from a console that already has one:

```
wslkit doctor
```

Elevation is detected, so no flag is needed. wslkit will not relaunch itself
to get it.

## Then fix

```
wslkit doctor fix <id>            print the plan
wslkit doctor fix <id> --apply    make the change
wslkit doctor undo                put it back
```

Nothing is applied on your behalf. Each finding that has a fix names it.

## When the filesystem is damaged

Take a copy before touching anything. An export is a tar of the files, so it
survives even when the filesystem underneath is suspect:

```
wsl --export <distro> backup.tar
```

WSL has its own small distribution, and it has `e2fsprogs` in it. It sees the
disk of every **running** distribution, so a read-only check needs nothing
installed and changes nothing:

```
wsl --system -u root -- lsblk -o NAME,SIZE,MOUNTPOINT
wsl --system -u root -- dumpe2fs -h /dev/sdX     # the superblock DSK004 read
wsl --system -u root -- e2fsck -n /dev/sdX       # check, change nothing
```

`-u root` is not optional: without it the block devices are not readable. Match
the device by size, and expect `e2fsck -n` to say the device is in use and that
it is skipping journal recovery — that is what read-only means here.

Repairing is the other half, and it writes. Stop the distribution first, because
a filesystem repaired underneath a running kernel is a filesystem broken twice:

```
wsl --terminate <distro>
wsl --mount --vhd <path-to-ext4.vhdx> --bare     # elevated console
wsl --system -u root -- e2fsck -fy /dev/sdX
wsl --unmount <path-to-ext4.vhdx>
```

`wslkit disk info <distro>` prints the path. If `e2fsck` cannot make the
filesystem consistent, the backup is the answer: `wsl --import` it into a fresh
distribution and let the damaged one go.

## If it still will not start

Save the machine and take it with you:

```
wslkit doctor --json > machine.json
```

That file can be replayed with `--from-snapshot` on any machine, which is how a
bug report becomes reproducible. See [reporting a bug](report-a-bug.md).
