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

## Trying a configuration before you commit to it

```
wslkit proxy show --pac http://localhost:8080/new-wpad.dat
wslkit proxy show --http proxy.corp.example:3128
```

`--pac` evaluates a script that is not the one Windows is configured with, which
is how you check a PAC file before deploying it to a fleet. `--http` and
`--https` name an address outright.

## What it does not do yet

Report only, for now. Writing the settings into a distribution so that systemd
units and apt see them, and running a local forward proxy that evaluates the PAC
per request, are the next two pieces.

In the meantime, `no_proxy` matters as much as the proxy itself: without
`localhost` in it, a distribution sends its own loopback traffic to the
corporate proxy and everything local breaks in a way that takes an afternoon to
understand. The list `proxy show` prints always carries it.
