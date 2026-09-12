# ADR 0006 — JSON schema versioning

Status: accepted, 2026-09-12.

`--json` emits `{"schema": "wsldoctor/result/v1", ...}` and the embedded environment
carries `"schema": "wsldoctor/env/v1"`. Until the M1 release the schema may change
freely. From M1 on, `v1` changes are additive only (new fields, new probe IDs, new enum
values are allowed; renames and removals are not). A breaking change bumps to `v2`;
for one minor release both are accepted by `--from-snapshot`.

Why: the JSON is the fixture format and the bug-report format. Old snapshots must keep
loading.
