# win10-defender-excluded

The same machine as `win10-defender-unprotected`, captured from an elevated
console after `wslkit doctor fix defender --apply`. Both distro disks and the
WSL processes are excluded, and DEF001 goes OK, naming the rule that covered
each disk.

It also carries 23 real `Host-Network-Service-Admin` events, every one id 1006
with `0x80070002`, on a machine where WSL starts every time. They are the reason
EVT001 no longer leads with host network errors that carry no HRESULT known to
stop WSL starting: doing so told a healthy machine to restart HNS and delete its
WSL network.
