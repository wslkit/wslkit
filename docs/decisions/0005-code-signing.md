# ADR 0005 — Code signing

Status: proposed, 2026-09-12. Needs the maintainer to open the SignPath Foundation
application; nothing in the build depends on it until the first tagged release.

Recommendation: SignPath Foundation (free for OSS, policy-gated, integrates with
GitHub Actions). Fallback: Azure Trusted Signing.

Why: the tool inspects Defender exclusions and touches `%LOCALAPPDATA%\wsl`, which is
exactly what SmartScreen and heuristics flag in unsigned binaries. Sign every release
binary; publish the certificate thumbprint in the README.
