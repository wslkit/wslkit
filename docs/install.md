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
gh attestation verify wslkit_0.1.0_windows_amd64.zip --repo wslkit/wslkit
```

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
a distribution. It is embedded in the binary, so the build fails without it.

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

Exit code 0 means it found nothing worth reporting, and 1 means it did. See
[exit codes](exit-codes.md) for the rest.

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
