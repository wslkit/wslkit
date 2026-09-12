# win10-2.7.13-healthy

Windows 10 22H2 (19045), WSL 2.7.13.0 (Store package), two modern-format distros
(Ubuntu 26.04 and a third-party alpine 3.24 distro), Defender real-time protection on,
collected **unelevated** on 2026-09-12. Everything works.

Expected: no FAIL. `WSL003 WARN` because 2.7.14 was already out; `DEF001 UNKNOWN`
because exclusions are admin-only to read (ADR 0007). The `results` array inside
`env.json` is informational; only `env` is loaded.
