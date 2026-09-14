# Proxies

Your company's proxy is configured in Windows. Everything on the Windows side
uses it. Then you open a shell in a distribution and `apt update` hangs, `pip`
times out, and `curl` sits there until you press Ctrl-C.

WSL does try. It reads your Windows proxy settings and puts `HTTP_PROXY` and
friends into the environment of every process it starts. That covers the shell
you just opened, and it stops there.

## What `wslkit proxy show` tells you

```
wslkit proxy show
```

It reads all three places Windows keeps a proxy setting — the per-user Internet
Settings a browser uses, the machine-wide WinHTTP configuration `netsh` writes,
and whatever a PAC script decides right now — and then says what a distribution
would actually be given:

```
Windows per-user settings (what a browser and WSL read)
  PAC script      http://wpad.corp.example/wpad.dat

Machine-wide WinHTTP settings (netsh winhttp, what services read)
  none

How a distribution reaches the host
  networkingMode  nat (the default)
  NAT gateway     172.20.240.1  in 172.20.240.0/20

What a distribution would be given
  http_proxy   http://proxy.corp.example:3128
  https_proxy  http://proxy.corp.example:3128
  no_proxy     localhost,127.0.0.1,::1,169.254.0.0/16,.corp.example
  source       Windows per-user Internet Settings: the PAC script
               http://wpad.corp.example/wpad.dat returned proxy.corp.example:3128
               for https://github.com/
```

## The three gaps it reports

Everything here is behaviour of the WSL runtime, not a guess.

**The variables are injected per process, and written to no file.** A systemd
unit does not see them. Neither does a cron job, a `[boot] command`, or a shell
that was already open when you changed the setting. This is the one that makes
people think the proxy "works sometimes".

**A PAC script is passed through, not evaluated.** WSL sets `WSL_PAC_URL` to
the address of the script and stops there, because nothing in a headless Linux
evaluates PAC. Nothing reads that variable, so on a machine whose proxy is
decided by a script there is no proxy inside the distribution at all.

`proxy show` evaluates it, through the same Windows component that a browser
uses. A script can answer differently for every host, so ask about the one you
care about:

```
wslkit proxy show --for https://internal.corp.example/
```

**A proxy on `127.0.0.1` is dropped.** In NAT mode the distribution has its own
loopback, so the host's is unreachable — and rather than rewriting the address
to the gateway, WSL discards the setting. A local proxy on Windows therefore
looks like no proxy at all. `proxy show` rewrites it and says so:

```
source  ...; the loopback address was rewritten to 172.20.240.1
        (NAT networking: the host is the WSL gateway 172.20.240.1)
```

## Writing it into a distribution

```
wslkit proxy apply -d Ubuntu --dry-run     what it would write
wslkit proxy apply -d Ubuntu               after one confirmation
wslkit proxy revert -d Ubuntu              take it all out again
```

`apply` writes five files, because each is read by something that reads none of
the others:

| File | What reads it |
|---|---|
| `/etc/wslkit/proxy.env` | the one file the others point at, and the one a systemd unit can watch |
| `/etc/environment` | PAM, so every login session and every `su` |
| `/etc/profile.d/99-wslkit-proxy.sh` | every login shell |
| `/etc/apt/apt.conf.d/99wslkit-proxy` | apt, which does not read the environment when it runs from a timer |
| `/etc/systemd/system.conf.d/wslkit-proxy.conf` | systemd, which builds its own environment for every unit |

The last one is the gap that costs the most. A machine where `curl` works in
your terminal and the systemd unit calling `curl` does not is the normal shape
of this problem.

Both spellings of every variable are written — `http_proxy` and `HTTP_PROXY`
and the rest. Which one a program reads is down to its library, and writing
only one is how a proxy ends up working for apt and not for pip.

### Reverting is exact

Everything written into a file that already existed goes inside a marked block:

```
# >>> wslkit proxy >>>
...
# <<< wslkit proxy <<<
```

`revert` removes the block and leaves the rest of `/etc/environment` exactly as
it was. Files that belong to wslkit entirely are deleted, and `/etc/wslkit`
goes too when nothing else is left in it. Applying twice changes nothing the
second time, rather than leaving two copies of every variable in a file where
nobody can tell which one is live.

### After applying

New shells have it immediately. Already-running processes keep the environment
they started with — that is true of every environment variable on every
operating system, and it is why `wsl --terminate <distro>` is the reliable way
to be sure.

For systemd units specifically, either terminate the distribution or run
`systemctl daemon-reexec` inside it. Verified: `systemctl show-environment`
then lists the proxy, so every unit started afterwards is proxied.

You probably also want `wsl2.autoProxy=false` in `.wslconfig`, so WSL stops
injecting its own copy of variables that are now in files. Two mechanisms
setting the same variable is how a stale proxy survives a change nobody can
find.

## When one answer is not enough: `proxy serve`

`apply` writes one proxy address into the distribution. That is right until the
right answer depends on the URL — which is exactly why PAC scripts exist. An
internal host goes direct, a build server has its own proxy, the rule changes
when the laptop moves. Nothing inside a distribution can evaluate a script.

`serve` runs a small forward proxy on the Windows side and asks Windows, per
request, the same question a browser would ask:

```
$ wslkit proxy serve
listening on 127.0.0.1:18080, 172.20.240.1:18080
upstream: asked per request, from Windows per-user Internet Settings

Point a distribution at it:
  wslkit proxy apply -d <distro> --http http://172.20.240.1:18080
```

Then point the distribution at it once, and never think about the proxy again:
the address inside the distribution stays the same whether you are in the
office, at home or on a train, because what changes is the answer the proxy
gets from Windows.

It handles both shapes a proxy sees: `CONNECT` tunnels for anything over TLS,
which is nearly everything, and plain forwarding for HTTP. Resolutions are
cached per host for a minute, so a build that downloads a thousand files
evaluates the script once.

### The firewall will be in the way

Measured on Windows 10 22H2: the host firewall blocks inbound connections from
the WSL subnet by default. A proxy listening on the gateway answers Windows
perfectly and is refused from inside the distribution, which is the confusing
way round.

```
$ wslkit proxy check -d Ubuntu
NAT networking: the host is the WSL gateway 172.20.240.1
Ubuntu: the distribution cannot open a connection to 172.20.240.1:18080

Allow it through, from an elevated PowerShell:
  netsh advfirewall firewall add rule name="wslkit proxy" dir=in action=allow protocol=TCP localport=18080 remoteip=172.20.240.0/20
```

Run that in an elevated window and `check` answers "can open a connection".
`curl` from inside the distribution then returns `200` over plain HTTP and real
content over an HTTPS tunnel, and with `proxy apply` pointing at it, both work
with no `--proxy` flag anywhere.

`check` asks from inside the distribution, because that is the only side whose
answer matters: before the rule, the same proxy answered `HTTP 200` to Windows
and refused the distribution outright. The rule is scoped to the WSL subnet, so
the port is not opened to whatever network you are on.

### What it will not do

**Authenticate to an upstream proxy with your Windows credentials.** If the
corporate proxy answers `407` with NTLM or Kerberos, this says so and stops:

```
the upstream proxy demands authentication (HTTP/1.1 407 Proxy Authentication
Required). wslkit cannot answer an NTLM or Kerberos challenge yet; a proxy that
authenticates with Windows credentials, such as px, can sit in front of it
```

Doing it properly means SSPI, and getting it half right would be worse than
saying plainly that it is not there.

### Trying it without Windows in the loop

```
wslkit proxy serve --direct           ignore the settings, go direct
wslkit proxy serve --upstream p:3128  send everything to one proxy
wslkit proxy serve --pac http://...   evaluate a script that is not the configured one
```

## Trying a configuration before you commit to it

```
wslkit proxy show --pac http://localhost:8080/new-wpad.dat
wslkit proxy show --http proxy.corp.example:3128
```

`--pac` evaluates a script that is not the one Windows is configured with, which
is how you check a PAC file before deploying it to a fleet. `--http` and
`--https` name an address outright.

## One last thing

`no_proxy` matters as much as the proxy itself.
Without `localhost` in it, a distribution sends its own loopback traffic to the
corporate proxy and everything local breaks in a way that takes an afternoon to
understand. The list wslkit writes always carries it.
