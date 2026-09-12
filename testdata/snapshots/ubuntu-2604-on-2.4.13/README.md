# ubuntu-2604-on-2.4.13 — the founding bug

Windows 10 22H2 (19045), WSL runtime **2.4.13.0**, one modern-format Ubuntu 26.04 distro
plus one third-party alpine-based modern distro. Every launch of Ubuntu failed with:

```
Catastrophic failure
Error code: Wsl/Service/E_UNEXPECTED
```

`wsl --update` to 2.7.13 fixed it. Root cause, derived from source and release notes:
Ubuntu 26.04 is cgroup v2 only and WSL runtimes before 2.5.1 mount a hybrid cgroup v1
hierarchy, so systemd cannot start (see `docs/research/2026-09-feature-research.md`, §1).
Note: `microsoft/WSL#13484` shows the same error string from a *different* cause
(corrupted VHD); it is not this bug.

Provenance: captured on the original machine after the fix with `wslkit doctor check --json`,
then the runtime version strings were rewritten from 2.7.13.0 back to 2.4.13.0 (the
registry and VHDX facts are otherwise unchanged). The `results` array inside `env.json` is
stale and ignored; only `env` is loaded.

Must detect: `WSL001 FAIL` ranked first, pointing at `fix update`.
