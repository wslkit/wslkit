# server2025-runner-no-distros

GitHub-hosted `windows-latest` runner (Windows Server 2025 Datacenter, build 26100, 8 GB
RAM), captured by the `ci-windows` workflow on 2026-09-12. WSL 2.7.13 runtime is
installed, VirtualMachinePlatform is enabled, Defender real-time protection is off, and
no distributions are registered.

What it guards: a machine with the runtime but no distro must produce a single
`WSL004 WARN` and no FAIL; every disk probe and WSL001 must report SKIPPED rather than
inventing a finding. The same runner produced a partial `Win32_OptionalFeature` answer on
a cold first query (HST001 briefly claimed VirtualMachinePlatform was disabled); that
case is handled by the collector retry and by HST001 mapping "not returned" to UNKNOWN.
