# Install

wslkit is a single executable with no installer and no dependencies. Download
it, put it somewhere on your path, and run it.

## Scoop

```powershell
scoop bucket add wslkit https://github.com/wslkit/scoop-wslkit
scoop install wslkit
```

That puts both `wslkit` and `wsldoctor` on your path. They are the same binary:
invoked as `wsldoctor` it runs `wslkit doctor`, so anything written against the
older name keeps working.

## By hand

Download the zip for your architecture from the
[releases page](https://github.com/wslkit/wslkit/releases), unpack it, and put
`wslkit.exe` somewhere on your path. There is nothing else in the archive but
the licence and the README.

The binaries are **not Authenticode-signed yet**, so SmartScreen warns the first
time you run one. Every release carries a build provenance attestation, which is
the stronger check anyway — it ties the archive to the exact workflow run and
commit that produced it:

```powershell
gh attestation verify wslkit_0.2.0_windows_amd64.zip --repo wslkit/wslkit
```

## Microsoft Defender

Two commands register a scheduled task that runs wslkit: `wslkit guard install`
and `wslkit disk automount install`. **Microsoft Defender currently quarantines
wslkit when it does that**, and removes the task as well.

Measured on 2026-10-05, Windows 10 22H2, security intelligence 1.459.561.0:
`guard install` was flagged as `Behavior:Win32/Persistence.A!ml` run from
`%TEMP%`, and as `Behavior:Win32/Execution.A!ml` run from
`%LOCALAPPDATA%\Programs`, within seconds either way. On 2026-09-14 the same
command was not flagged. It is a behaviour verdict on an unsigned binary with
little reputation, and signing ([#19](https://github.com/wslkit/wslkit/issues/19))
is the lasting fix. Nothing else wslkit does has been flagged.

Until then both commands refuse and explain why, unless you add `--force`. If
you do and Defender flags it, open *Windows Security > Virus & threat
protection > Protection history*, choose the entry for `wslkit.exe`, and
*Allow on device*. That restores the file; run the install command again
afterwards, because the task was removed with it.

## From source

Go 1.27 or later and nothing else; there is no C compiler in the picture,
because the binary uses no cgo.

```
git clone https://github.com/wslkit/wslkit
cd wslkit
go run ./tools/build-agent -version dev
go build -o wslkit.exe ./cmd/wslkit
```

The first command builds the Linux guest agent that `wslkit agent` installs into
a distribution. It is embedded in the binary. Without it the build still
succeeds, but `wslkit agent install` says the agent is not embedded.

## What it runs on

Windows 10 build 19041 or later, on amd64 or arm64. WSL 2 itself is what needs
that floor; wslkit inherits it.

It works with both the Microsoft Store build of WSL and the in-box one, and it
tells you which you have. Several checks report differently depending on the
answer, so it matters.

## Checking it works

```
wslkit version
wslkit doctor
```

`doctor` reads only. It touches no configuration, starts no distribution and
asks for no administrator, so it is safe to run first and safe to run often.

Exit code 0 means no check failed, though warnings may still be shown, and 1
means at least one did. See [exit codes](exit-codes.md) for the rest.

## Tab completion

```
wslkit completion powershell | Out-String | Invoke-Expression
source <(wslkit completion bash)
source <(wslkit completion zsh)
```

Add the first line to your PowerShell profile to keep it. Distribution names
are resolved when you press Tab rather than baked into the script, so one
generated last month still knows about a distribution installed this morning.

## The old name

wslkit began as `wsldoctor`. The binary is multi-call: a copy or shim named
`wsldoctor.exe` behaves exactly as `wslkit doctor`, so anything that called the
old name keeps working.
