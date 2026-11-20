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

`winget validate` and `winget install --manifest` take a directory holding
only manifests, and this one also holds this README, so copy them out first.
From the repository root, in PowerShell:

```powershell
$v = "0.4.0"; $d = "$env:TEMP\wslkit-winget\$v"
New-Item -ItemType Directory -Force $d | Out-Null
Copy-Item packaging\winget\*.yaml $d
winget validate --manifest $d
winget install --manifest $d     # needs: winget settings --enable LocalManifestFiles (admin)
```

Then open the pull request: the three files go to
`manifests/w/wslkit/wslkit/<version>/` in a fork of `microsoft/winget-pkgs`,
titled "New package: wslkit.wslkit version <version>" the first time and
"Update: wslkit.wslkit to <version>" after that, with the repository's pull
request template filled in. `wingetcreate submit --token <your-token> <dir>`
does the same in one step.

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

## No `wsldoctor` alias

The binary also answers to `wsldoctor`, its older name: invoked under that name
it runs `wslkit doctor`. winget cannot offer both, because a portable package
may alias each file only once ("Duplicate relative file path found"), so the
manifests alias `wslkit` only. Anyone who wants the old name can copy or link
`wslkit.exe` to `wsldoctor.exe`.
