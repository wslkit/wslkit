# sock

`wslkit sock` gives a distribution the Windows SSH and GPG agents. Your keys
stay on Windows, including hardware-backed ones, and Linux tools use them
unchanged.

```
wslkit sock list
wslkit sock enable ssh-agent -d Ubuntu
wslkit sock status -d Ubuntu
```

It needs the [guest agent](agent.md) installed and its daemon running.

## Why this rather than copying keys

A key copied into a distribution is a key in two places, on a filesystem that
several tools can read and that a `wsl --export` will carry off the machine. A
hardware key cannot be copied at all.

Bridging means the private key never leaves Windows. The distribution gets a
Unix socket that speaks the agent protocol, and the agent on the Windows side
does the signing.

## Presets

Each preset creates a Unix socket inside the distribution and relays it to
something on Windows.

| Preset | Bridges | Sets |
|---|---|---|
| `ssh-agent` | the Windows OpenSSH agent, or anything on that named pipe | `SSH_AUTH_SOCK` |
| `gpg-agent` | the gpg4win agent | the GnuPG agent socket |
| `gpg-agent-ssh` | gpg4win's SSH support | `SSH_AUTH_SOCK` |
| `gpg-agent-extra` | the restricted gpg4win socket, for forwarding onward | the extra socket |

`wslkit sock list` prints what each one bridges on your machine, including where
it found your GnuPG home.

The `ssh-agent` preset works with anything that serves the standard named pipe,
which includes 1Password and several other agents as well as the one built into
Windows.

## Enabling one

```
wslkit sock enable ssh-agent -d Ubuntu
```

This writes a file into `/etc/profile.d` in the distribution that exports the
variable, and records the listener so the daemon knows to serve it. Open a new
shell in the distribution and `ssh-add -l` should list your Windows keys.

The name may come before or after the flags; both are things people type.

## Disabling one

```
wslkit sock disable ssh-agent -d Ubuntu
```

Removes the listener and regenerates the profile script from what is left.

## When it does not work

```
wslkit sock status -d Ubuntu
wslkit agent status
```

The usual cause is that the Windows agent is not running. For OpenSSH that is
the `ssh-agent` service, which is disabled by default on Windows; for GnuPG it
is `gpg-connect-agent /bye`.

`sock enable` warns rather than refuses when it cannot find the named pipe,
because the agent may start later. It does refuse a missing GnuPG socket file,
because that one has to exist before there is anything to point at.

See [ADR 0010](https://github.com/wslkit/wslkit/blob/main/docs/decisions/0010-sock-presets.md)
for why these four presets and not a general forwarding mechanism.
