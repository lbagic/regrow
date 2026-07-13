# Architecture

Status: skeleton (Phase 0). This stub records structure and invariants; it grows with each phase. Product rationale lives in [PRODUCT.md](PRODUCT.md); build order in [PLAN.md](PLAN.md).

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
