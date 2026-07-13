# Architecture

Status: Phase 1 in progress (engine + schema + TUI done; rule catalog port next). This doc records structure and invariants; it grows with each phase. Product rationale lives in [PRODUCT.md](PRODUCT.md); build order in [PLAN.md](PLAN.md).

## Shape

Single static Go binary. Bubble Tea + Lip Gloss TUI. Rules are declarative YAML embedded at build time; code exists only for the weird cases (Docker df parsing, HF scan-cache, simctl).

```
cmd/regrow/        entrypoint, flag parsing, --json mode
internal/engine/   rule schema, loader, dry-run planner
internal/scanner/  size measurement, marker discovery, tool queries
internal/tui/      checklist UI, plan screen
internal/trash/    trash-not-rm + staging fallback, path guards
internal/oplog/    jsonl journal, undo, history
rules/             *.yaml catalog (go:embed), --rules-dir override
```

## Data flow

scan → findings (semantic: what, why, regen story, cost, last-used) → user selection in TUI → **plan screen** (exact commands + trash destinations) → execute → oplog → undoable.

## Invariants (the fence)

1. Dry-run is the default; execution is opt-in per run.
2. Nothing is ever `rm`'d directly: trash first, staging dir fallback, `--rm` explicit opt-out.
3. Every destructive path passes the guard: never empty, `/`, `$HOME`, or mount roots.
4. Native steward commands over raw deletion (`go clean -cache`, `ollama rm`, `docker builder prune`, `simctl runtime delete`).
5. Risk classes are architectural, not cosmetic: `safe` (auto-clean), `caution` (review), `surface-only` (never deletable through us).
6. Every executed action lands in `~/.local/state/regrow/oplog.jsonl` before it runs.

## Testing

Fixture-$HOME builder + golden dry-run snapshots per rule. CI: macOS runner (Linux joins in v0.3). New rules ship behind `--beta-rules`.

## Decisions log

- 2026-07-13 — name: `regrow` (tentative; evidence in PLAN.md Phase 0). Module `github.com/lbagic/regrow`.
- 2026-07-13 — Go + Bubble Tea over Rust/shell: single binary, best TUI ecosystem, GoReleaser + brew tap path is proven (PRODUCT.md §6).
- 2026-07-13 — rule schema (Prompt C): version-aware paths are per-entry `{path, os_min, os_max}` (inclusive, bare string = unconstrained) rather than versioned OS keys — one entry per path move, matches research §14's changelog shape. Unknown host version ⇒ constrained entries do not apply (conservative).
- 2026-07-13 — tool queries (docker df, simctl, hf, ollama) are *named* code hooks in `internal/scanner`; rules reference them via `tool_query`. Keeps YAML pure data while parsing stays per-tool code.
- 2026-07-13 — path guards live in `internal/trash` and are enforced by the *planner*, not the UI: surface-only rules and guard-rejected paths become plan `Skipped` entries with reasons. Guard also rejects all top-level dirs (`/Applications`), not just `/` and mount roots.
- 2026-07-13 — sizes are physical blocks (`Stat_t.Blocks*512`, du-style), never logical — sparse files and APFS clones must report what deletion actually reclaims.
- 2026-07-13 — golden harness: fixture-$HOME → scan embedded catalog → plan; snapshot is the normalized *command list* (sizes and temp paths stripped) in `internal/scanner/testdata/golden/`. Refresh via `go test ./internal/scanner -run Golden -update`.
- 2026-07-13 — TUI (Prompt D): `regrow scan` opens the Bubble Tea checklist only when stdin *and* stdout are TTYs; piped/`--json` output is unchanged so scripting never breaks. Scan runs async behind a spinner. Safe findings start pre-selected; caution needs a tick; surface-only rows have no checkbox and space is a no-op — but the *planner* stays the enforcement point (the TUI passes selection through `engine.BuildPlan`, which skips surface-only regardless). Plan screen is terminal in Phase 1: enter shows exact commands, nothing executes. UI model is testable headless (scan fn injected, key msgs driven in tests). Shared display helpers (`HumanBytes`, `ShellJoin`) live in `internal/tui` and are reused by the plain CLI printer.
