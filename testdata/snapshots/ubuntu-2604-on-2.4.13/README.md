# ubuntu-2604-on-2.4.13 — the founding bug

Windows 10 22H2 (19045), WSL runtime **2.4.13.0**, one modern-format Ubuntu 26.04 distro
plus one third-party alpine-based modern distro. Every launch of Ubuntu failed with:

```
Catastrophic failure
Error code: Wsl/Service/E_UNEXPECTED
```

`wsl --update` to 2.7.13 fixed it. Public issue: https://github.com/microsoft/WSL/issues/13484

Provenance: captured on the original machine after the fix with `wsldoctor check --json`,
then the runtime version strings were rewritten from 2.7.13.0 back to 2.4.13.0 (the
registry and VHDX facts are otherwise unchanged). The `results` array inside `env.json` is
stale and ignored; only `env` is loaded.

Must detect: `WSL001 FAIL` ranked first, pointing at `fix update`.
