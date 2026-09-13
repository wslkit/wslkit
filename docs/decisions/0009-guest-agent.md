# ADR 0009 — Guest agent: delivery and transport

Status: accepted, 2026-09-13. Implements issue #30; research in
`docs/research/2026-09-subcommands.md` §0.

## Decision

1. **One static Linux binary per distribution**, `cmd/wslkit-agent`, cross-compiled for
   linux/amd64 and linux/arm64 by `tools/build-agent`, embedded into `wslkit.exe`
   (`internal/agent/payload`) and extracted into `/usr/local/lib/wslkit/` by
   `wslkit agent install -d <distro>` over `wsl.exe -d <distro> -u root`. A systemd
   system unit (`wslkit-agent.service`) keeps it running; without systemd the install
   reports the gap and stops.
2. **Transport: Hyper-V sockets, guest connects, Windows listens.** The Windows daemon
   (`wslkit agent serve`) binds `AF_HYPERV` on the running VM's id with the VSOCK template
   service id, port 51000. An unelevated process can do this without any registry
   registration (spike `spikes/hvsock`); host-initiated connects into the guest were not
   reliable in the spike and are not used. Streams in either direction are multiplexed
   over that one guest-initiated connection (`internal/agent/mux`, protocol in
   `internal/agent/proto`, version 1).
3. **VM id discovery is passive.** The id changes on every `wsl --shutdown`; the daemon
   reads it from the `--vm-id` argument of the running `wslhost.exe` / `wslrelay.exe`
   processes through WMI (`internal/agent/vmid`), never by starting a distribution.
4. **Allow list on the Windows side.** The daemon only opens targets listed in
   `%LOCALAPPDATA%\wslkit\agent\host.json`; every other OPEN is refused with a message the
   guest can show. Traffic filters (e.g. the ssh-agent extension filter) are keyed by
   target in the same file.
5. **No admin anywhere** in the default path: install runs as the distro's root through
   wsl.exe (the user's own session), the daemon is a per-user process, autostart is the
   per-user `Run` key.

## Testing

Hosted runners have no WSL 2, so the transport is exercised in unit tests over TCP and
`net.Pipe`: protocol framing (fuzzed nightly), multiplexer, relay with half-close and
filters, Windows daemon (allow list, refusal, status file) and guest agent (listener
relay, reconnect, refusal while disconnected). The Linux runner builds and tests the
agent natively; the Windows runners build the payload and run the daemon tests. The
hvsock path is verified manually with `wslkit agent install`, `start`, `status` against
a real distribution before release.

## Consequences

- `wslkit.exe` grows by the two embedded agent binaries.
- Every agent-based subcommand (`sock`, `limit`, `top`, `proxy`, `log`) adds listeners to
  the guest config and targets to the host allow list; none needs its own transport.
- If Microsoft changes the helper command lines or the template GUID scheme, VM id
  discovery or binding breaks; `wslkit agent status` and the AGT001 doctor probe make that
  visible.
