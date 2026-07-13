# Changelog

## Unreleased

- TUI (Prompt D): Bubble Tea checklist — findings grouped by category, size-ranked, risk-colored (safe/caution/surface), regen story + cost for the row under the cursor, safe rules pre-selected, space to toggle, enter opens the plan screen (exact commands, trash destinations, skips with reasons) — nothing executes. `regrow scan` opens the TUI on a TTY; piped or `--json` keeps plain output.
- Rule engine (Prompt C): YAML schema {id, title, category, risk, version-aware per-OS paths, marker discovery, tool_query, native_command, regen, sudo}, strict loader with `--rules-dir` override, du-style size scanner + marker discovery + tool-query registry, dry-run planner emitting the exact command list with path guards and surface-only enforcement. CLI: `regrow scan|plan|rules|version`, `--json`. First 3 rules: xcode-derived-data, go-build-cache, rust-target-dirs. Golden-plan test harness.
- Scaffold: Go module, package layout, CI (macOS test + linux lint).
