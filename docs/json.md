# JSON output

Every reporting command takes `--json`. The output is meant to be consumed, and
these rules hold across the whole kit.

## The rules

**One object per line, not an array.** A command that reports many things emits
one object per line, so a consumer can process the stream as it arrives and a
long run is not buffered to the end.

**Sizes are integers, in bytes.** Never a human-readable string, never a unit
suffix. Formatting is the reader's job.

**Absent is not zero.** A field that could not be measured is omitted entirely.
A running distribution genuinely has no readable virtual disk size, and
reporting `0` would be a lie rather than a gap. Check for the key, not for a
zero value.

**Keys are sorted.** Output is stable between runs, so a diff of two captures
shows what changed rather than what moved.

**Errors are on standard output too.** Under `--json` a failure is an object in
the same stream, carrying a stable token and the exit code. See
[exit codes](exit-codes.md).

**`--verbose` never reaches standard output.** It goes to standard error, so it
cannot corrupt the stream.

## doctor

`wslkit doctor --json` emits one object under the schema name
`wslkit/result/v1`. Adding a field is not a breaking change; removing or
repurposing one is, and would come with a new schema name.

That output is also an input:

```
wslkit doctor --json > machine.json
wslkit doctor --from-snapshot machine.json
```

Every check runs against the saved machine rather than the live one. That is how
a bug report becomes reproducible, and it is how the checks are tested.

Each collected fact carries where it came from and, when it could not be read,
why. A field is not simply missing: it records whether the value was absent, the
access was denied, or the read failed.

## disk

One object per distribution for `list`, one for `info`, one per target for
`compact`, one per file for `orphans`.

```
wslkit disk list --json
```

```json
{"allocated_bytes":2707423232,"default":true,"file_size":2707423232,"flavor":"ubuntu","guid":"{...}","name":"Ubuntu","os_version":"26.04","size_on_disk":2707423232,"sparse":false,"version":2,"vhdx_path":"C:\...\ext4.vhdx","virtual_size":1099511627776}
```

`reclaimable` appears only when both of the numbers it is derived from could be
measured, and it is floored at zero: on a compressed volume the guest's figure
can legitimately exceed what the file costs.

## top

One object, with a `vm` block, a list of distributions, and a list of `groups`:
the cgroups that belong to no distribution, such as WSL's own processes and
`/docker`. Counters a distribution could not report are left out rather than
written as zero. With `--watch --json`, one such object per line, one per
interval.

It carries a `method` field saying how memory was attributed and a `note`
explaining what that method leaves out, because the per-distribution figures do
not sum to the VM total and a consumer should not have to guess why. See
[top](top.md).

## Dry runs

A command asked for `--dry-run --json` emits a plan object: the subject,
`dry_run: true`, the steps it would take, and any warnings. Nothing is
measured and nothing is changed.
