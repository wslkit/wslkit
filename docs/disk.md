# disk

`wslkit disk` inspects and maintains the virtual disks behind WSL 2
distributions. It is the Go reimplementation of
[wsldisk](https://github.com/wslkit/wsldisk), which is archived.

Nothing here needs an administrator, except attaching extra disks with
`automount now` and `automount install`.

```
wslkit disk list                     what each distribution costs
wslkit disk usage Ubuntu             where the space inside it went
wslkit disk compact Ubuntu           trim, stop, then shrink the file
wslkit disk trash Ubuntu             unregister it, but keep the disk
wslkit disk rename Ubuntu Work       rename it
wslkit disk snapshot Ubuntu          copy its disk aside, to restore later
```

## Which number is which

Three sizes get confused, and the difference matters.

**Size on disk** is what the volume actually spends on the file. It is the
number that changes when you free space, and the one Explorer shows.

**Virtual size** is the maximum the disk is allowed to grow to. It is normally
1 TiB whatever the distribution holds, and it never changes. A disk that reports
a terabyte is not using a terabyte.

**Guest used** is what the filesystem inside the distribution says it is using.
The gap between it and size on disk is roughly what compaction could reclaim.

## Reporting

### list

Every distribution, its disk, and what could be reclaimed.

```
wslkit disk list
wslkit disk list --json      integer bytes, one object per line
wslkit disk list --probe     start stopped distributions to read guest usage
```

`--probe` is off by default because starting a distribution to measure it
changes the thing being measured. Without it, a stopped distribution reports a
dash for the guest columns rather than a zero, which would be a lie.

### info

Everything known about one distribution: its registration, its disk geometry,
and its guest usage.

```
wslkit disk info Ubuntu
wslkit disk info Ubuntu --probe
```

### usage

Where the space inside a distribution went, against a catalogue of paths that
commonly hold reclaimable data.

```
wslkit disk usage Ubuntu
wslkit disk usage Ubuntu --by-directory     plus the whole guest by directory
wslkit disk usage Ubuntu --top 10
wslkit disk usage Ubuntu --by-directory --depth 3
```

It reports only. It never deletes anything.

A row marked as not clearable does not mean dangerous. It means wslkit cannot
judge on your behalf: a Docker storage directory is not a cache, and whether
what it holds still matters is not something a disk tool can know.

Entries that contain other entries are counted once, so the total never claims
more space than the guest is using.

## Reclaiming

### trim

Asks the filesystem inside the distribution to release the blocks it is no
longer using. This is what makes compaction worth running.

```
wslkit disk trim Ubuntu
```

The figure it reports is the free extent of the disk, not space reclaimed. On a
1 TiB virtual disk holding 12 GiB it says about a tebibyte every time. The
output says so; compaction is what shrinks the file.

This starts the distribution if it is stopped, and leaves it running.

### compact

Trims, stops the distribution, waits for the disk, then rewrites the file
without the unused blocks.

```
wslkit disk compact Ubuntu
wslkit disk compact --all --shutdown
wslkit disk compact --file D:\disks\docker_data.vhdx
wslkit disk compact Ubuntu --dry-run
```

| Flag | What it does |
|---|---|
| `--all` | every WSL 2 distribution |
| `--file PATH` | a loose `.vhdx`, such as the one Docker Desktop keeps |
| `--no-trim` | skip the trim. Compaction then reclaims almost nothing |
| `--restart` | start the distribution again afterwards if it was running |
| `--shutdown` | permit stopping every distribution to free the disk |
| `--unlock-timeout D` | how long to wait for the utility VM to let go |
| `--trim-timeout D` | how long to let fstrim run |

Stopping a distribution does not free its disk. The utility VM holds it for
about a minute afterwards, so the command waits. While any other distribution is
running the VM never idles out at all, which is what `--shutdown` is for.

**Compaction can reclaim nothing, legitimately.** It works in whole VHDX blocks,
so free space scattered in small holes leaves every block partly occupied. A
result of zero is a real answer, not a failure.

## Moving and repairing

### Compacting on a schedule

```
wslkit disk compact --all --auto -y
```

Compaction has to stop the distribution, so an unattended run in its plain form
interrupts someone's work to reclaim a few megabytes nobody would have chosen
to be interrupted for. `--auto` is the rule that makes it safe to schedule:

- a disk that needs nothing stopped is compacted — there is nothing to weigh
  against it;
- a running distribution is stopped only when at least `--min-reclaim` (1 GiB
  by default) can actually be reclaimed;
- a stopped distribution whose disk the utility VM is holding open for another
  one is skipped, because freeing it means stopping everything.

Either way it says what it decided, which is what you want to read in a log the
next morning:

```
Ubuntu                   skipped: how much is reclaimable cannot be read without starting it
skrog-engine             skipped: 222.2 MiB reclaimable is below the 1.0 GiB worth stopping it for

nothing was worth compacting
```

Nothing worth doing exits 0, so a scheduled task does not report a failure for a
quiet week. With `--json` each skip is a line of its own, with its reason.

### move

Moves a disk to another directory or drive and repoints the registration.

```
wslkit disk move Ubuntu D:\wsl
wslkit disk move Ubuntu D:\wsl --keep-source
```

The copy preserves the holes in the disk, so a 12 GiB file stays 12 GiB rather
than becoming the terabyte it is nominally allowed to reach.

The original is deleted last, and only after the distribution has been proved to
start from its new location. If it does not start, everything is put back.

### rebuild

```
wslkit disk rebuild Ubuntu                  export, import, keep the registration
wslkit disk rebuild Ubuntu --keep-archive   keep the archive as a backup
wslkit disk rebuild Ubuntu --dry-run        the plan, in full
```

Compaction works in whole blocks of the virtual disk. Free space scattered
through the filesystem in small holes leaves most of those blocks partly used,
so a disk that is half empty inside can reclaim almost nothing — and the
command that reclaims nothing is the one that makes people give up.

Exporting and importing writes every file afresh into a new disk, in order, and
the holes are gone. Measured on a test distribution that compaction could do
little with: 684 MiB down to 204 MiB.

Doing it by hand loses the registration. `wsl --import` makes a new GUID and
drops the default user, the flags and the default-distribution marker, so the
distribution comes back opening a root shell instead of yours. `rebuild` puts
them back.

The order is what makes it safe: the archive is written before anything is
deleted, and it is kept wherever anything goes wrong, with the `wsl --import`
line that recovers from it. The one thing it cannot put back is the GUID
itself: anything that recorded the old one, such as a plugin registration,
will not find it.

### orphans and relink

```
wslkit disk orphans                          disks no distribution claims
wslkit disk orphans --scan D:\wsl            look somewhere else as well
wslkit disk orphans --delete                 after one confirmation
wslkit disk relink Ubuntu D:\wsl\ext4.vhdx   repoint a distribution
```

Unclaimed is not unused. Docker Desktop keeps a disk holding every volume you
have, and no distribution claims it. `orphans` says so before it deletes
anything, refuses any file that is open, and leaves alone any file whose state
it cannot determine.

The listing names the software each disk belongs to where the path says:

```
SIZE ON DISK  BELONGS TO      PATH
28.4 GiB      Docker Desktop  C:\Users\you\AppData\Local\Docker\wsl\disk\docker_data.vhdx
1.9 GiB       unknown         D:\backups\old-ubuntu.vhdx
```

Those disks can be compacted without being deleted, and without copying a path
from one command into another:

```
wslkit disk compact --orphans               after one confirmation
wslkit disk compact --orphans --dry-run     what it would do
```

Compacting rewrites the file and frees the unused blocks inside it; nothing in
it is lost. A disk another application is using is still in use, though, so it
may have to be stopped first — Docker Desktop's disk will refuse while Docker
is running.

`relink` writes registry values and touches no file. It starts the distribution
to check the new path works, and puts the registry back if it does not.

### rename

```
wslkit disk rename Ubuntu Work
```

WSL has no command for this (microsoft/WSL#4241). The name is one value in the
distribution's registry key, and the WSL service reads it live: measured on
WSL 3.0.1, the new name works at once and the old one stops resolving.
`rename` writes it, starts the distribution under the new name to prove it, and
puts the old name back if that fails. `wslkit doctor undo` reverses it later.

Names follow WSL's own rule, measured against `wsl --import`: letters, digits,
`.`, `-` and `_`. The distribution has to be stopped.

What breaks is anything that uses the old name: `\\wsl.localhost\<old>` paths
and scripts running `wsl -d <old>`. The Windows Terminal profile and the
Start-menu shortcut WSL makes for a modern distribution keep working, because
both launch it by GUID, and only go on showing the old name.

### flags and default-user

```
wslkit disk flags Ubuntu                                   show them
wslkit disk flags Ubuntu --append-path=off --automount=off
wslkit disk default-user Ubuntu zoe                        or a uid
```

These are the switches on the distribution's registry key that `wsl.exe` has
no command for. WSL reads them as the distribution starts, so a change to a
running one applies after `wsl --terminate`. The undocumented fourth bit WSL
sets is carried over untouched. Every change goes into the undo journal.

Measured on WSL 3.0.1: `--append-path=off` takes the Windows directories out
of `PATH`, and `--automount=off` leaves `/mnt/c` empty. `--interop=off` is
weaker than it sounds. Sessions lose their own interop socket (`WSL_INTEROP`
is empty), but `cmd.exe` still starts. To stop Windows programs launching, set
`[interop] enabled=false` in the distribution's `/etc/wsl.conf`, which does.

`default-user` takes a name only while the distribution is running, because
turning a name into a uid means asking the distribution, and starting it to ask
would be a side effect. A stopped one takes a uid, and is told the uid was not
checked.

### snapshot and restore

```
wslkit disk snapshot Ubuntu --name before-upgrade
wslkit disk snapshot list
wslkit disk restore Ubuntu before-upgrade      or an id, or latest
wslkit disk snapshot rm Ubuntu <id>
```

A snapshot is a copy of the disk file, holes kept, in
`%LOCALAPPDATA%\wslkit\snapshots\<guid>\<id>`, with a manifest beside it. They
are copies rather than differencing disks: WSL attaches whatever file its
registration names, and its own `--manage --move` and `--resize` would break a
chain silently. They are kept by GUID, so a renamed distribution keeps them,
and they are refused when the volume does not have room.

`restore` moves the current disk aside, copies the snapshot into place, and
starts the distribution to prove it boots. If it does not, the disk it replaced
goes back. If it does, the replaced disk becomes a snapshot of its own, so a
restore is itself undoable, by `wslkit doctor undo` or by restoring that one.

The disk has to be free to copy it. Once a distribution has run, the WSL utility
VM keeps its disk open until no distribution is running at all (measured on
3.0.1: still held 40 seconds after it stopped, while another one ran). So both
`snapshot` and `restore` take `--shutdown`, which is often needed, and which
stops everything.

### Attaching extra disks

```
wslkit disk automount add D:\disks\data.vhdx --name data
wslkit disk automount list
wslkit disk automount now          from an elevated terminal
wslkit disk automount install      from an elevated terminal, once
wslkit disk automount rm D:\disks\data.vhdx
wslkit disk automount uninstall    remove the logon task
```

`wsl --mount <disk> --vhd` attaches a disk until WSL next shuts down, and there
is no setting that attaches one every time (microsoft/WSL#11187). `automount`
keeps a table of them in the settings file, as `[[automount]]` blocks, and
attaches the table: now, or at every logon from a scheduled task.

It only attaches. Nothing formats, partitions or writes to a disk. A disk the
table's run finds already open is left alone, so running it twice is harmless.
It refuses a registered distribution's own disk, which would give one ext4
filesystem two writers.

`wsl --mount` needs an elevated process, so `now` does too, and the logon task
runs with highest privileges. Registering a task like that needs an elevated
terminal once. The task writes what it did to
`%LOCALAPPDATA%\wslkit\automount.log`.

### Distributions the Store installed

A distribution installed from the Store, or from any app package, belongs to
that package as well as to WSL. Its registration names the package
(`PackageFamilyName`), its disk lives in
`%LOCALAPPDATA%\Packages\<package>\LocalState`, and the app's launcher finds it
by name. Moving the disk, repointing it, rebuilding it under a new GUID or
unregistering it leaves the app launching into nothing, and WSL itself does
not refuse any of them: `wsl --manage --move` checks only that the
distribution is stopped.

So `move`, `relink`, `rebuild`, `trash` and `rename` refuse such a distribution, before
anything has changed, and say how to remove it properly: through
*Settings > Apps*, which removes the app and the distribution together.
`--force` overrides the refusal, for when you know the app no longer needs it.

`disk list` marks these distributions `[store]`, and `disk info` names the
package. The disk counts as the package's if either the registration names a
package or the disk is in a package directory. When only one of the two says
so, `info` adds a note: a package name with the disk elsewhere means it has
already been moved out from under the package, and the doctor's DSK002 warns
about that too.

## Undoing an unregister

`wsl --unregister` deletes the disk along with the registration, without asking,
and fires its notification only afterwards, so nothing can intercept it.

```
wslkit disk trash Ubuntu                       unregister it, but keep the disk
wslkit disk undelete Ubuntu                    register it again
wslkit disk trash --list                       what is in the trash, and how old
wslkit disk trash --purge --older-than 30d     free the space for good
```

`trash` stops the distribution, records everything its registration says, moves
the disk to `%LOCALAPPDATA%\wslkit\trash`, and only then unregisters, so WSL
finds nothing left to delete.

`undelete` imports the disk where it lies and puts back the default user, the
flags, and its place as the default distribution. Those last are stored outside
the distribution's own registry key and would otherwise be lost.

## Settings

`%APPDATA%\wslkit\config.toml`, written by the tool and safe to edit by hand.

```
wslkit disk config                          what is set, and where
wslkit disk config set compact.trim false
wslkit disk config get compact.trim         the bare value, for scripts
wslkit disk config edit
```

| Key | Default | What it does |
|---|---|---|
| `scan.dirs` | empty | extra `orphans` roots, semicolon-separated |
| `compact.trim` | `true` | trim before compacting |
| `compact.restart` | `false` | restart a distribution that was running |
| `wsl.unlock_timeout_seconds` | `90` | how long to wait for the disk |

A flag given on the command line always beats the setting, in either direction.
