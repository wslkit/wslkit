# Contributing

The full guide is
[CONTRIBUTING.md](https://github.com/wslkit/wslkit/blob/main/CONTRIBUTING.md).
This is enough to get a change in.

## Building

```
git clone https://github.com/wslkit/wslkit
cd wslkit
go run ./tools/build-agent -version dev
go build -o wslkit.exe ./cmd/wslkit
```

The first command builds the Linux guest agent that gets embedded in the
binary. The build fails without it.

## Before you push

```
gofmt -l ./cmd ./internal ./tools
GOOS=linux go vet ./internal/probe/... ./internal/disk/ ./internal/top/
GOOS=windows go vet ./...
go test ./...
golangci-lint run
```

Both `vet` runs matter. The Linux one is what enforces the separation between
collecting and deciding, and it catches a whole class of mistake that a Windows
build does not: pure code reaching for a Windows API, or joining a Windows path
with the host's separator.

CI runs the linter with `GOOS=windows`, so the Windows-only files are linted
too. Running it yourself saves a round trip.

## What a change needs

**A test that would have failed.** Not coverage for its own sake: a test that
states the behaviour, in a name that reads as a sentence.

**A comment where the reason is not obvious.** Especially where something looks
wrong and is not. A great deal of this codebase is working around WSL behaviour
that only makes sense with the explanation attached, and those comments are the
most valuable thing in the repository.

**No AI attribution.** Commits, pull requests and issues carry no generated-by
trailers or footers.

## Documentation

`docs/` is the source of truth. It is plain markdown that renders on GitHub and
reviews as a diff; this site is a view of it.

```
go run ./tools/gen-docs          stage docs/ into site/content
hugo server -s site              preview
```

Every page must appear in a section of `site/data/nav.yaml`. A page nothing
links to is a page nobody reads, so the generator fails on one rather than
letting it become reachable only by URL.

The reference pages are generated from the data files and registries and must
not be edited. Change the data, not the page.

## Decisions

A change that rules something out, or that costs something worth knowing about
later, gets a record in `docs/decisions/`. They are short: the decision, why,
and what it costs. A decision that turns out to be wrong is superseded by a
later record rather than edited, so the reasoning survives.

## Adding a check

Checks live in `internal/probe`. A check is a pure function of the collected
facts: it performs no I/O, and it must compile and pass on Linux.

If it needs a fact nothing collects yet, add it to the collector with its
provenance, so a check can distinguish "false" from "could not read".

Add a snapshot that exercises it. The test suite runs every check against saved
machines, which is what makes them testable at all.
