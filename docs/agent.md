# agent

Some things cannot be done from Windows alone. `wslkit agent` puts a small
helper inside a distribution and runs a Windows-side daemon it talks to, so a
Linux process can reach something on Windows without either side needing
administrator rights.

Today its one user is [`wslkit sock`](sock.md), which bridges the Windows SSH
and GPG agents into a distribution.

```
wslkit agent install -d Ubuntu --autostart
wslkit agent start
wslkit agent status
```

## How it is put together

The guest agent is a static Linux binary, built for amd64 and arm64 and embedded
in `wslkit.exe`. Installing it writes the binary into the distribution and, on a
systemd distribution, a unit that starts it.

The two sides talk over a Hyper-V socket. The guest connects out and the Windows
daemon listens, which is the direction that works without registering anything
and without an administrator.

A stream is opened per connection and multiplexed over that one socket. What the
daemon will open on the Windows side is a fixed list you have enabled; it is not
a general tunnel, and nothing you have not allowed is reachable.

See [ADR 0009](https://github.com/wslkit/wslkit/blob/main/docs/decisions/0009-guest-agent.md)
for the design and what it deliberately rules out.

## Commands

| Command | What it does |
|---|---|
| `agent install -d <distro>` | put the agent in a distribution |
| `agent uninstall -d <distro>` | take it out again |
| `agent start` | start the Windows daemon |
| `agent stop` | stop it |
| `agent status` | whether the daemon is up and the agent is connected |
| `agent serve` | run the daemon in the foreground |
| `agent autostart` | start the daemon when you log in |
| `agent vm-id` | the id of the running utility VM |

`--autostart` on `install` does the install and the login registration together.

## Checking it

```
wslkit agent status
```

Tells you whether the daemon is running, whether the agent has connected, and
which distribution it came from. If a socket preset is not working, this is the
first thing to look at: the bridge needs both halves up.

## Removing it

```
wslkit sock disable <preset> -d Ubuntu
wslkit agent uninstall -d Ubuntu
wslkit agent stop
```

Disabling the presets first leaves nothing pointing at a socket that is about to
stop existing.
