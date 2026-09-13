# A dev server that never reloads

You edit a file in VS Code, save it, and nothing happens. The dev server is
running, the browser is open, the build tool says it is watching. It just never
notices.

This is not your tool being broken. It is where the files are.

## Why it happens

Your project is on the Windows filesystem, reached from Linux as
`/mnt/c/...`. Watching files on Linux means asking the kernel, through inotify,
to say when something under a directory changes. The kernel can only answer that
for a filesystem it owns.

`/mnt/c` is not one. It is a protocol to a server on the Windows side, and that
server has no way of saying "this file just changed". The watch is accepted —
nothing returns an error — and then it never fires.

[microsoft/WSL#4739](https://github.com/microsoft/WSL/issues/4739), the issue
tracking this, was locked in 2024. There is no setting inside the distribution
that turns it on, and no version of WSL where it starts working.

## Find out whether it is you

```
wslkit doctor check
```

`MNT001` looks at what is running in each started distribution and reports any
watcher-shaped tool whose working directory is on a Windows drive:

```
WARN    MNT001  Ubuntu: vite is watching /mnt/c/src/app
        Switch it to polling: server.watch.usePolling = true in vite.config
```

It reads `/proc` from the Windows side, so it sees only what is running right
now, and only in a distribution that is already started. A distribution that is
stopped is reported as not looked at rather than as clean.

## The two fixes

### Move the project into the distribution

```
mv /mnt/c/src/app ~/app
```

Watching works again, and everything else gets faster: file access inside the
distribution's own filesystem is not going over a protocol at all. This is the
fix, and it is what Microsoft's own documentation recommends.

You can still open the project from Windows. In VS Code, the WSL extension
opens `~/app` inside the distribution; Explorer reaches it at
`\\wsl.localhost\Ubuntu\home\you\app`.

### Or make the tool poll

If the project has to stay on the Windows side — a shared checkout, a licence
tied to a path, a colleague's build script — every watcher has a way to check
the files on a timer instead of waiting to be told:

| Tool | Setting |
|---|---|
| anything using chokidar (nodemon, webpack, jest, gulp, parcel, Angular) | `CHOKIDAR_USEPOLLING=1` |
| vite | `server.watch.usePolling = true` |
| webpack directly | `watchOptions.poll = 1000` |
| jekyll | `jekyll serve --force-polling` |
| hugo | `hugo server --poll 700ms` |
| cargo-watch | `cargo watch --poll` |
| air | `poll = true` under `[build]` |
| entr | `ENTR_INOTIFY_WORKAROUND=1` |
| dotnet watch | `DOTNET_USE_POLLING_FILE_WATCHER=1` |
| tsc | `tsc --watch --watchFile priorityPollingInterval` |

Polling costs CPU, and on a large tree it costs a lot of it: every interval,
every file gets stat'd over the same protocol that was already the slow part.
It works, which is more than the alternative does, but it is the second-best
answer.

`watchman` has no polling mode at all. Neither does `inotifywait`: it is a thin
wrapper over the kernel facility that is not available here, so a loop around
`find -newer` is the closest equivalent.

## What about the other direction

A file changed inside the distribution, watched by a Windows program, has the
same problem in reverse and the same answer: keep the files on the side that
watches them.
