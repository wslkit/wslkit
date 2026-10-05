# When systemd or the user session is broken

With `[boot] systemd=true` in a distribution's `/etc/wsl.conf`, WSL starts
systemd as PID 1, and systemd starts a session for your user when you open a
shell: `user@<uid>.service`, `/run/user/<uid>`, the session bus, and
`XDG_RUNTIME_DIR` pointing at that directory. When any of it fails, what you see
is one line at launch:

```
wsl: Failed to start the systemd user session for '<user>'.
```

and then a shell where `systemctl --user` does not work and everything that
wants a session bus or a socket in `XDG_RUNTIME_DIR` fails in its own way:
podman rootless, pipewire, gpg-agent, `wslkit sock`. That one line covers
several different faults (microsoft/WSL#13826, #13562, #13848).

`SYS001` tells them apart, in each distribution that is running:

```
wslkit doctor
```

| It says | What it means |
|---|---|
| `boot.systemd=true, but PID 1 is init` | wsl.conf asks for systemd and it is not running. wsl.conf is read at start, so the change needs `wsl --terminate <name>`; if that has happened, the image may not have systemd at all |
| `systemd is degraded, N failed unit(s)` | `systemctl is-system-running` says degraded; the failed units are named |
| `systemd reports "maintenance"` (or another state) | neither running nor degraded |
| `the systemd user session for uid N is not running` | your processes exist, but `user@N.service` does not. This is the upstream thread. If no file under `/etc/pam.d` loads `pam_systemd`, it says so |
| `default uid N is not in /etc/passwd` | the registration's default user does not exist in the distribution |
| `/run/user/N does not exist`, or the wrong owner or mode | the session runs, but its directory is missing or not yours (`700`) |
| `XDG_RUNTIME_DIR is X, not /run/user/N`, or not set | your processes have the wrong value, or none. A shell profile that sets it is the usual cause |

What is not a fault, and is shown only in the detail:

- systemd not enabled at all.
- **degraded only because `systemd-binfmt.service` failed.** That unit fails on
  Ubuntu under WSL as a rule (it did on WSL 3.0.1 here), and its failure is the
  better outcome. When it succeeds it can drop WSL's own handler for Windows
  executables, and `.exe` files stop launching (microsoft/WSL#8843).
- no process of your user running yet, so there is no session to judge. Open a
  shell in the distribution and run the doctor again.
- a default user of root.

## How it checks

It runs one read-only script as root inside each running distribution, through
`wsl.exe`, and never starts a stopped one: that would start systemd, which is
the thing being checked. It does not log in as you to see your environment,
because a login goes through `pam_systemd` and would start the very session it
is asking about. Your environment is read from your existing processes instead.

It reports and explains, and fixes nothing: adding `pam_systemd` to a PAM stack,
or masking a unit, is not something to do behind your back.
