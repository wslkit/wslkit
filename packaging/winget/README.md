# winget manifests

The three manifests winget wants, for the current release. They are kept here
rather than only in `microsoft/winget-pkgs` so that what was submitted is
visible in this repository, and so a submission can be prepared without waiting
for anybody.

## Submitting them

Publishing to winget means opening a pull request against
[microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs), a repository
this project does not own. That is deliberately not automated here: it needs a
fork under a real account and a token with rights to push to it, and neither
belongs to a workflow that anybody can trigger.

To do it by hand:

```powershell
winget install wingetcreate
wingetcreate submit --token <your-token> packaging\winget
```

or, to check them first without submitting anything:

```powershell
winget validate --manifest packaging\winget
wingetcreate submit --prtitle "New package: wslkit.wslkit version 0.1.0" packaging\winget
```

`winget validate` needs the manifests in a directory of their own, which is what
this is.

## Keeping them current

`tools/gen-winget` rewrites all three from a published release, taking the
hashes from that release's own `checksums.txt` rather than computing them from a
download that might have gone wrong:

```
go run ./tools/gen-winget -version 0.1.0
```

The release workflow runs it after publishing and commits the result, so nobody
has to remember. It cannot run before the tag: the manifests carry the release's
URLs and hashes, and inventing those for archives nobody has built yet is not a
thing a generator can do.

## The two aliases

`NestedInstallerFiles` lists `wslkit.exe` twice, under `wslkit` and
`wsldoctor`. It is one binary: invoked as `wsldoctor` it runs `wslkit doctor`,
so anything written against the older name keeps working.
