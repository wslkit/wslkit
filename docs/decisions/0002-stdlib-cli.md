# ADR 0002 — Standard-library CLI, no framework

Status: accepted, 2026-09-12.

`cmd/wsldoctor` uses `flag` and a hand-written subcommand router
(`check`, `fix`, `undo`, `explain`, `preflight`, `version`). No cobra/viper/urfave.

Why: a diagnostic people run once should start in milliseconds and have a dependency
graph a reviewer reads in one sitting. The subcommand surface is small and stable.
