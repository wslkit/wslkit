wslkit is one binary of tools for WSL 2 on Windows. It diagnoses why WSL is
broken or slow, reclaims the space its disks are holding, and bridges Windows
into a distribution.

```
wslkit doctor                    read-only, no administrator, ranked diagnosis
wslkit doctor explain "Wsl/Service/E_UNEXPECTED"
wslkit disk list                 what each distribution costs on disk
wslkit disk compact Ubuntu       trim, stop, then shrink the file
wslkit top                       what the utility VM is using, and which distribution
```

No installer, no PowerShell, no administrator for anything that only reads.
Nothing changes unless you name a command that changes it, and every change
prints its plan first and records a rollback.
