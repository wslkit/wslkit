# Files ending in :Zone.Identifier

Every so often a directory inside a distribution fills up with pairs:

```
installer.deb
installer.deb:Zone.Identifier
report.pdf
report.pdf:Zone.Identifier
```

The second file of each pair is one line of metadata. Nothing inside Linux
created it, nothing on Windows shows it, and nothing will ever clean it up.

## Where they come from

When you download a file, Windows records where it came from. On NTFS that
record is an *alternate data stream* attached to the file rather than a file of
its own, which is why you never see it: it is part of `installer.deb`, not next
to it.

Save that download into `\\wsl.localhost\Ubuntu\...` and the stream has nowhere
to go. The file server behind that path serves a Linux filesystem, which has no
alternate streams, so it writes the stream out as an ordinary file named after
the original with `:Zone.Identifier` on the end.

The same thing happens when you drag a file from a browser's download bar into
an Explorer window pointing at the distribution, and when a Windows program
saves into `\\wsl.localhost`.

They are harmless in themselves. What they break is everything that treats a
directory as a list of files: a `for f in *` loop that now runs twice per
download, a build that copies them into an image, a checksum over a directory
that no longer matches.

## Find them

```
wslkit doctor check
```

`ZON001` reports how many there are in each running distribution, with a few
examples:

```
WARN    ZON001  Ubuntu: 34 Zone.Identifier file(s)
```

The scan looks under `/home` and `/root` only, skips the usual large
machine-generated directories such as `node_modules`, and is time-boxed. A
distribution that is not running is not scanned at all: reading its files from
Windows would start it, which a check that only reports is not entitled to do.

## Delete them

```
wslkit doctor fix zone --distro Ubuntu
```

That prints the exact list and does nothing else. Add `--apply` to delete them:

```
wslkit doctor fix zone --distro Ubuntu --apply
```

Only the metadata files are touched; the downloads themselves are left alone.
`--path /home/ana/Downloads` narrows it to one directory.

This one is not reversible. What is lost is the record of which zone a download
came from, which Windows uses to decide whether to warn you before running it.
Inside Linux nothing reads it.

## Avoiding them

Download to the Windows side and move the file in from a shell inside the
distribution, where the stream is dropped rather than written out:

```
cp /mnt/c/Users/you/Downloads/installer.deb ~/
```

Or delete them as you go:

```
find ~ -name '*:Zone.Identifier' -delete
```

## Why Windows and Linux disagree about the name

A colon is not allowed in a Windows filename, so the file server does not store
one. It keeps the character out of the way at U+F03A, in the Unicode private use
area, which is the same trick Services for UNIX used. The name reads as
`report.pdf:Zone.Identifier` inside the distribution; from Windows the colon is
a private use character that most fonts draw as a blank or a box, so searching
for a literal colon finds nothing at all.

That is worth knowing if you go looking for these files yourself: in PowerShell,
`Get-ChildItem -Filter '*:Zone.Identifier'` matches nothing, because by the time
the filter runs the colon has been read as the start of a stream name.
