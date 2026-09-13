# ADR 0010 — `wslkit sock`: bridged sockets and presets

Status: accepted, 2026-09-13. Implements issue #31 on top of the guest agent
(ADR 0009); research in `docs/research/2026-09-subcommands.md` §1.

## Decision

1. **Presets, not free-form mappings, are the interface.** `wslkit sock enable
   <preset> -d <distro>` wires one known service. The preset table
   (`internal/sock`) names the guest socket path, the Windows target, the
   environment variables to export and any traffic filter. Free-form mappings can
   come later; every early request in the upstream issues is one of these four.
2. **All presets run guest-client to Windows-service.** That is the direction the
   transport supports (ADR 0009) and the direction every agent protocol needs.
3. **Wiring lives in three places, all rewritten from one source of truth.** The
   guest listener list in `/etc/wslkit/agent.json`, the Windows allow list and
   filter map in `host.json`, and `/etc/profile.d/99-wslkit-sock.sh`, which is
   regenerated from the enabled listeners so `disable` cannot leave a stale
   `SSH_AUTH_SOCK` behind.
4. **A missing named pipe is a warning, not a failure.** The Windows agent may
   start later and the bridge picks it up. A missing gpg-agent socket file *is* an
   error, because the port and nonce cannot be resolved without reading it.
5. **The ssh-agent filter stays.** OpenSSH 8.9+ clients send
   `SSH_AGENTC_EXTENSION`, which older Windows agents answer by dropping the
   connection; the filter answers `SSH_AGENT_FAILURE` locally so the client falls
   back instead of failing.

## Testing

Preset expansion, listener construction, profile rendering, target parsing and
gpg4win socket discovery are unit-tested and run on every CI job. The relay
itself is covered by the agent tests (ADR 0009). The end-to-end path was verified
on a real machine with a purpose-built named pipe (`spikes/npipe-echo`): a
connection to the guest AF_UNIX socket reached the Windows pipe and the reply
came back, with the daemon reporting one opened stream.

## Consequences

- Enabling a preset restarts the guest agent, which drops in-flight streams for
  that distribution. Acceptable: it happens only on an explicit command.
- `SSH_AUTH_SOCK` is exported through `profile.d`, so existing shells need to be
  reopened, and a user who sets it elsewhere wins.
- `ssh-agent` and `gpg-agent-ssh` both claim `SSH_AUTH_SOCK`; enabling both is
  allowed but the last file wins, so the preset notes say to pick one.
