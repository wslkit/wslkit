# disk

`wslkit disk` inspects and maintains the virtual disks behind WSL 2
distributions. It is the Go reimplementation of
[wsldisk](https://github.com/wslkit/wsldisk), which is archived.

Nothing here needs an administrator.

```
wslkit disk list                     what each distribution costs
wslkit disk usage Ubuntu             where the space inside it went
wslkit disk compact Ubuntu           trim, stop, then shrink the file
wslkit disk trash Ubuntu             unregister it, but keep the disk
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

Stopping a distribution does not free its disk. The utility VM holds it for
about a minute afterwards, so the command waits. While any other distribution is
running the VM never idles out at all, which is what `--shutdown` is for.

**Compaction can reclaim nothing, legitimately.** It works in whole VHDX blocks,
so free space scattered in small holes leaves every block partly occupied. A
result of zero is a real answer, not a failure.

## Moving and repairing

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

`relink` writes registry values and touches no file. It starts the distribution
to check the new path works, and puts the registry back if it does not.

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
