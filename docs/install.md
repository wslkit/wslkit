# Install

wslkit is a single executable with no installer and no dependencies. Download
it, put it somewhere on your path, and run it.

There are no releases yet, so for now it is built from source. That needs Go
1.27 or later and nothing else; there is no C compiler in the picture, because
the binary uses no cgo.

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
