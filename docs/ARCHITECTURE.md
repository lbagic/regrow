# Architecture

Status: Phase 2 complete (execution, trash/undo, oplog, release pipeline); wedge features (Phase 3) next. This doc records structure and invariants; it grows with each phase. Product rationale lives in [PRODUCT.md](PRODUCT.md); build order in [PLAN.md](PLAN.md).

## Shape

Single static Go binary. Bubble Tea + Lip Gloss TUI. Rules are declarative YAML embedded at build time; code exists only for the weird cases (Docker df parsing, HF scan-cache, simctl).

```
cmd/regrow/        entrypoint, flag parsing, --json mode
internal/engine/   rule schema, loader, dry-run planner
internal/scanner/  size measurement, marker discovery, tool queries
internal/tui/      checklist UI, plan screen
internal/trash/    trash-not-rm + staging fallback, path guards, receipts
internal/oplog/    jsonl journal (grouping, undoable computation)
internal/executor/ runs plans across the trash/oplog seams; undo
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
- 2026-07-13 — catalog port (Prompt E): `native_command` accepts a plain string (whitespace-split) or an explicit argv list — needed for arguments with spaces (osascript scripts, find patterns). New optional `note` field carries footgun warnings (shown in the TUI footer) so regen.story stays purely about regeneration. Path entries may contain globs (`Install macOS*.app`), expanded by the scanner at measure time.
- 2026-07-13 — `Host.Root` re-anchors absolute rule paths so the golden fixture can fake `/Library` and `/Applications`; empty/`/` means the real filesystem. Home is never re-anchored. This keeps "every rule gets a golden test" true for system-path rules.
- 2026-07-13 — tool queries shipped: docker df (aggregate reclaimable per type; the daemon tracks last-used only for build cache, so per-image "unused for N months" aging is a later extension — `Item.LastUsed` already carries the data when a query can supply it), simctl devices/runtimes (simctl-only, per research §6; runtimes filtered to `deletable`), tmutil snapshot list (sizes unknowable pre-thin, items carry 0 bytes). A missing tool or stopped docker daemon means "rule does not apply", not a scan error. Parsers are pure functions unit-tested on canned output; the golden test injects fake queries.
- 2026-07-13 — risk calls during the port: `trash-empty` is caution (clean.sh said safe) because emptying the Trash destroys regrow's own undo buffer; `library-updates` is surface-only (root-owned, only cleanable by raw rm as root — outside the trash-first fence); `docker-volumes` stays caution-with-loud-note since volume data is not regenerable; ~/Library/Caches is surface-only pending per-app itemization.
- 2026-07-13 — `DirSize` never fails on walk errors, root included: TCC-protected dirs (~/.Trash, CoreSpotlight) Lstat fine but refuse ReadDir without Full Disk Access; the target exists and reports a partial (possibly 0) size, `du 2>/dev/null`-style.
- 2026-07-13 — trash seam placed before Phase 2 (architecture review, docs/plans/2026-07-13-architecture-review.md): `internal/trash` owns the trash mechanism; the planner obtains the Finder-move preview via `trash.PreviewCommand` instead of building osascript itself. Actions keep carrying the exact argv (plan/--json contract unchanged). Move/receipt/staging signatures are deliberately NOT stubbed — designed in Phase 2 when the executor (their caller) exists.
- 2026-07-13 — golden harness v2 (supersedes the shared-snapshot harness above): fixture data lives in each rule's YAML (`fixture:` — files in rule path syntax, fake tool-query items, optional host os/version override; default darwin/15.5). Each rule scans an isolated fixture home → `testdata/golden/<rule-id>.golden`. Hard coverage: embedded rules without a fixture, with scan errors, or matching zero items fail; stray goldens fail. `Fixture` is `json:"-"` and inert at runtime; Validate does not require it, so `--rules-dir` users aren't forced to write fixtures — the test enforces it for the embedded catalog only.
- 2026-07-13 — placeholder convention owned by the schema (Prompt F, review candidate 4): `Argv.Placeholders/PerItem/ExpandItem` live in `internal/engine/schema.go`; `Validate` rejects unknown `{token}`s at load and `{arg}` on rules without a tool query. Expansion refuses empty values (`simctl delete ""` must never form); the planner skips such items with a reason.
- 2026-07-13 — trash execution (Prompt F): the Finder osascript *returns the trashed item's POSIX path* (Finder renames on collision — "x 14.54.11"; the receipt must record where the item landed, verified against the real Finder). Preview and execution are the same argv by construction. Staging fallback (`~/.local/state/regrow/staging/<run-id>/`) is a plain rename — works on read-only trees (go mod cache) since only parents matter; cross-volume targets fail loudly and are out of scope for v0.1. `Restore` refuses to overwrite a regenerated original. TCC note: `~/.Trash` refuses *listing* without Full Disk Access but rename in/out of it by known path works — undo needs no FDA.
- 2026-07-13 — oplog protocol (Prompt F): two lines per action — `start` (synced *before* the action runs, invariant 6; journal failure refuses to act) then `done` (with trash receipt) or `fail`; `undo` lines reference the same (run, seq). `Run.Undoable` = done receipts minus successful undos, reverse execution order. Corrupt journal lines fail loudly — the journal is the undo contract, skipping lines could undo the wrong thing. One failed action never aborts a run: a half-finished run is exactly what undo makes safe.
- 2026-07-13 — `regrow clean` defaults (Prompt F): no ids = safe rules only (the TUI's pre-selection); caution rules must be named explicitly. Plan printed + confirmed (y/N on TTY, `--yes` when piped). `--rm` opt-out from PRODUCT.md deferred — no demand until someone asks. TUI plan screen stays terminal and points at `regrow clean`; in-TUI execution can land later behind the same planner/executor.
- 2026-07-13 — release (Prompt F): GoReleaser darwin arm64/amd64, homebrew *cask* (brews stanza deprecated upstream) into `lbagic/homebrew-tap` with a quarantine-strip hook, checksums signed keyless via cosign/GitHub-OIDC — "signed release" without the $99 Apple certificate, which waits for the GUI companion decision (Phase 5).
- 2026-07-13 — TUI (Prompt D): `regrow scan` opens the Bubble Tea checklist only when stdin *and* stdout are TTYs; piped/`--json` output is unchanged so scripting never breaks. Scan runs async behind a spinner. Safe findings start pre-selected; caution needs a tick; surface-only rows have no checkbox and space is a no-op — but the *planner* stays the enforcement point (the TUI passes selection through `engine.BuildPlan`, which skips surface-only regardless). Plan screen is terminal in Phase 1: enter shows exact commands, nothing executes. UI model is testable headless (scan fn injected, key msgs driven in tests). Shared display helpers (`HumanBytes`, `ShellJoin`) live in `internal/tui` and are reused by the plain CLI printer.
