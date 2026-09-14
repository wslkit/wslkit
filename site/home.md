wslkit is a toolkit for WSL 2 on Windows: troubleshooting and maintenance
commands in one binary. It diagnoses why WSL is broken or slow, reclaims the
space its disks are holding, caps what one distribution may use, and bridges
Windows into a distribution.

```
wslkit doctor                    read-only, no administrator, ranked diagnosis
wslkit doctor explain "Wsl/Service/E_UNEXPECTED"
wslkit disk list                 what each distribution costs on disk
wslkit disk compact Ubuntu       trim, stop, then shrink the file
wslkit top                       what the utility VM is using, and which distribution
wslkit limit set -d Ubuntu --high 3GB --cpus 2
```

No installer, no PowerShell, no administrator for anything that only reads.
Nothing changes unless you name a command that changes it, and every change
prints its plan first and records a rollback.
