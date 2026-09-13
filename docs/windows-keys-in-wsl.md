# Use your Windows keys inside WSL

Your SSH and GPG keys can stay on Windows, where a hardware token or a password
manager already holds them, and still sign and authenticate from inside a
distribution.

The alternative most people reach for is copying the private key into the
distribution. That puts it in two places, on a filesystem several tools can
read, and a `wsl --export` carries it off the machine. A hardware key cannot be
copied at all.

## Set it up

Two commands, neither needing an administrator:

```
wslkit agent install -d Ubuntu --autostart
wslkit sock enable ssh-agent -d Ubuntu
```

The first puts a small helper inside the distribution and registers the Windows
daemon to start when you log in. The second creates a Unix socket in the
distribution and relays it to the Windows agent.

Open a new shell in the distribution:

```
ssh-add -l
```

That should list the keys your Windows agent holds. `SSH_AUTH_SOCK` is exported
by a file in `/etc/profile.d`, so it is set for new shells rather than the one
you were already in.

## For GPG, and for signing commits

```
wslkit sock enable gpg-agent -d Ubuntu
```

Bridges the gpg4win agent, so `git commit -S` inside the distribution signs with
the key Windows holds.

If you prefer gpg4win's SSH support to OpenSSH's agent:

```
wslkit sock enable gpg-agent-ssh -d Ubuntu
```

`wslkit sock list` prints all four presets and what each bridges on your
machine.

## When nothing is listed

```
wslkit sock status -d Ubuntu
wslkit agent status
```

The bridge has two halves and both have to be up.

The usual cause is the Windows agent not running. The OpenSSH agent ships with
Windows but its service is disabled by default:

```
Get-Service ssh-agent | Set-Service -StartupType Automatic
Start-Service ssh-agent
ssh-add                     # add a key to it
```

For GnuPG, `gpg-connect-agent /bye` starts the agent and creates its socket.

`sock enable` warns rather than refuses when it cannot find the OpenSSH pipe,
because the agent may start later. A missing GnuPG socket is a hard error,
because there is nothing to point at until it exists.

## Turning it off

```
wslkit sock disable ssh-agent -d Ubuntu
wslkit agent uninstall -d Ubuntu
```

Disable the presets first, so nothing is left pointing at a socket that is about
to stop existing.
