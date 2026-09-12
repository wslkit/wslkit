# win11-arm64-no-wsl

GitHub-hosted `windows-11-arm` runner (Windows 11 Enterprise 25H2, build 26200, arm64),
captured by the `ci-windows` workflow on 2026-09-12. No WSL runtime is installed; only
the in-box installer stub `C:\Windows\System32\wsl.exe` (version 10.0.26100.x) exists.
`VirtualMachinePlatform` and, notably, `Microsoft-Windows-Subsystem-Linux` both report
as enabled, and there is no `LxssManager` service.

What it guards: exactly one FAIL, `WSL002` "WSL is not installed", ranked first. An
earlier build produced two FAILs here (a spurious `HST002` about `LxssManager`) and
mislabelled the stub as legacy in-box WSL because it trusted the optional-feature state.
The service's existence, not the feature, is the signal for legacy WSL.
