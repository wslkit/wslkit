# Reclaim disk space

WSL disks grow and do not shrink on their own. Deleting a file inside a
distribution frees space inside the distribution and changes nothing on Windows.

Getting it back is two steps, and it helps to know which is which.

## Find out where it went

```
wslkit disk list
```

The **reclaimable** column is the gap between what the file costs on your volume
and what the distribution says it is using. That is roughly what step two can
give back.

Then look inside:

```
wslkit disk usage Ubuntu
wslkit disk usage Ubuntu --by-directory
```

This checks a catalogue of paths that commonly hold reclaimable data: package
caches, language toolchain caches, logs, container storage. It only reports.

A row marked as not clearable is not a warning. It means wslkit cannot judge:
a Docker storage directory is not a cache, and whether what it holds still
matters is not a disk tool's call.

## Clear what you do not want

Inside the distribution, and this part is yours to do:

```
sudo apt clean                 # or dnf, pacman, apk
npm cache clean --force
go clean -modcache
docker system prune
journalctl --vacuum-size=100M
```

wslkit never deletes anything inside a distribution.

## Shrink the file

```
wslkit disk compact Ubuntu
```

This does both halves of the job. It asks the filesystem to discard the blocks
it is no longer using, stops the distribution, waits for the utility VM to
release the disk, then rewrites the file without those blocks.

Without the discard step, compaction reclaims almost nothing: the disk image
still holds the stale data, whatever the filesystem inside thinks.

To see what it would do first:

```
wslkit disk compact Ubuntu --dry-run
```

## When it reclaims nothing

That is a real answer, not a failure. Compaction works in whole blocks, so free
space scattered in small holes leaves every block partly occupied and there is
nothing to remove.

An export and re-import rewrites the filesystem from scratch and can reclaim
what compaction cannot, at the cost of the registration and a lot of I/O. It is
tracked as a possible `rebuild` command.

## When something is holding the disk

Stopping a distribution does not free its disk. The utility VM keeps it for
about a minute, so the command waits.

While **any** distribution is running the VM never idles out at all, so a second
distribution, or Docker Desktop, blocks the whole thing. The refusal names what
is holding it:

```
wslkit disk compact Ubuntu --shutdown
```

stops everything first. That includes your containers, which is why it is opt-in
rather than automatic.

If WSL is already down and the file is still held, the remaining suspects are a
backup agent, an antivirus scanner, or Hyper-V Manager.

## Disks nothing is using

```
wslkit disk orphans
```

Finds `.vhdx` files that no distribution claims, in the places WSL and Docker
put them.

Unclaimed is not unused. Docker Desktop keeps a disk holding every volume you
have and no distribution claims it. Check what each one is before deleting
anything, which is why `--delete` prints what it found and asks once.
