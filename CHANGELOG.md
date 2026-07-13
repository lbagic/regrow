# Changelog

## Unreleased

- Rule engine (Prompt C): YAML schema {id, title, category, risk, version-aware per-OS paths, marker discovery, tool_query, native_command, regen, sudo}, strict loader with `--rules-dir` override, du-style size scanner + marker discovery + tool-query registry, dry-run planner emitting the exact command list with path guards and surface-only enforcement. CLI: `regrow scan|plan|rules|version`, `--json`. First 3 rules: xcode-derived-data, go-build-cache, rust-target-dirs. Golden-plan test harness.
- Scaffold: Go module, package layout, CI (macOS test + linux lint).
