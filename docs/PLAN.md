# Zero → Hero Plan

Each phase = one or a few Claude sessions with a ready prompt. Milestones (M1–M4) mark when the thing becomes *usable*, *shareable*, *launchable*, *fundable*.

## Status

<!-- Living tracker. Every session that finishes a prompt ticks its box and moves the "Now" line. Enforced via CLAUDE.md session rules. -->

- [x] P0-A name → **regrow** (2026-07-13)
- [x] P0-B scaffold — repo pushed, CI green (2026-07-13, github.com/lbagic/regrow)
- [x] P1-C engine + schema (2026-07-13)
- [x] P1-D TUI (2026-07-13)
- [x] P1-E port clean.sh + Tier-S rules → **M1 usable** (2026-07-13, 30 rules, real scan found 158.8 GiB)
- [x] Architecture review + Prompt-F groundwork (2026-07-13): trash seam, per-rule golden fixtures, scan cancellation — [review doc](plans/2026-07-13-architecture-review.md)
- [x] P2-F safety hardening (2026-07-13): clean/undo/history, trash+staging+receipts, oplog, placeholder validation, --beta-rules, GoReleaser+release CI — code done; **M2 needs manual publish** (tap repo, TAP_GITHUB_TOKEN secret, tag v0.1.0)
- [x] P3-G0 item identity (2026-07-13): `ruleID/key` ids, per-item plan/clean selectors + `Plan.Unmatched` typo guard, TUI expandable item rows (all/partial/none), `--json` keys, refits: sim devices/runtimes, xcode-archives (per-archive glob), tm-snapshots (per-snapshot delete) — [design doc](plans/2026-07-13-item-identity.md)
- [ ] P3-G ML models module
- [ ] P3-G2 docker detection + targeting
- [ ] P3-H doctor + phantom space → **M3 launchable**
- [ ] P4-I launch kit → **M4 public**

**Now:** Prompt G (ML models) or G2 (docker) — both unblocked by G0. Still pending manual: publish v0.1 (create `lbagic/homebrew-tap` repo, add `TAP_GITHUB_TOKEN` secret, `git tag v0.1.0 && git push --tags`); buy regrow.sh; revisit the `regrow` name before tagging (P0-A said revisit before M2).

```mermaid
flowchart TD
    P0[Phase 0: Decisions<br/>name · repo · license] --> P1[Phase 1: Skeleton<br/>engine + TUI + 15 safe rules]
    P1 --> M1{{M1: USABLE FOR ME<br/>replaces clean.sh daily}}
    M1 --> P2[Phase 2: Safety hardening<br/>trash/undo · golden tests · CI]
    P2 --> M2{{M2: SHAREABLE<br/>brew tap, friends use it}}
    M2 --> P3[Phase 3: Wedge<br/>item identity · ML models · docker<br/>doctor · phantom space]
    P3 --> M3{{M3: LAUNCHABLE}}
    M3 --> P4[Phase 4: Launch<br/>README GIF · Show HN · socials]
    P4 --> M4{{M4: PUBLIC<br/>sponsors on}}
    M4 --> P5[Phase 5: Growth<br/>Android/JVM/IDEs · Linux · npx<br/>homebrew-core · GUI companion?]
```

---

## Phase 0 — Decisions (1 short session)

**Prompt A — name:**
> Analyze name candidates for the cleaner product (reclaim, regen, hoard, disksmith, sweeper + generate 10 more). Check collisions: brew formula, npm, crates.io, GitHub repos, domains (.dev/.sh), App Store. Criteria: verb-like, typeable, hints "everything deleted regenerates", no snake-oil vibes. Recommend top 3 with evidence.

✅ **Decided (2026-07-13): `regrow`** — tentative, revisit before M2/brew tap. Evidence: crates.io free, brew free, regrow.sh free, npm squatted but dead (8 dl/mo, last publish 2022 — disputable), no GitHub presence (top repo ★23). Runners-up: mow (domains taken), cull (no regen hint). Wildcard if regrow falls through: disksmith (100% clean everywhere, but not verb-like).

**Prompt B — scaffold decisions:**
> Init the repo: git, MIT license, Go module (name from Prompt A), directory layout for: rule engine (rules/*.yaml embedded), scanner, TUI (bubbletea), trash/undo, oplog. Write ARCHITECTURE.md stub. No features yet — structure + CI skeleton (GitHub Actions, macOS runner, lint+test).

Exit: name chosen, repo pushed, CI green on hello-world.

## Phase 1 — Skeleton → **M1: usable for me**

**Prompt C — engine + schema:**
> Implement the rule engine per docs/PRODUCT.md §6: YAML rule schema {id, title, risk, os paths (version-aware), marker discovery, native command, regen story, sudo}, loader, size scanner (du + tool queries), dry-run planner producing exact command list.

**Prompt D — TUI:**
> Bubbletea checklist TUI per PRODUCT.md §4 sketch: grouped by category, size-ranked, risk colors, note on cursor, plan screen before any action. `--json` output mode.

**Prompt E — port clean.sh:**
> Port all modules from clean.sh into YAML rules + add Tier-S from docs/research/02: DerivedData, DeviceSupport, CoreSimulator devices+runtimes (simctl only), docker prune, npm/yarn/pnpm, go, brew, pip/uv, TM snapshots, aerial (Sequoia+Tahoe paths), trash. Golden test per rule with fixture $HOME.

**M1 check:** `regrow` run on this Mac finds ≥60GB, executes via trash, undo works. clean.sh retired.

## Phase 2 — Safety hardening → **M2: shareable**

**Prompt F:**
> Harden: path guards (empty//`/`/$HOME/mounts), trash-not-rm with staging fallback, oplog jsonl + `undo` + `history`, fixture-home golden tests for every rule, race/permission edge cases (read-only go mod cache, root sim runtimes), `--beta-rules` gate. Then: GoReleaser config, brew tap repo, signed release v0.1.

Pre-done (2026-07-13, [architecture review](plans/2026-07-13-architecture-review.md)): per-rule golden coverage shipped; trash seam placed — `internal/trash` owns the mechanism (`PreviewCommand`), planner emits intent. Build Move/receipts/staging behind that seam; design their signatures with the executor, they were deliberately not stubbed. Also in scope now that commands execute: placeholder validation (review candidate 4 — a `{arg}` typo silently downgrades a per-item command; must fail at load, not execution).

**M2 check:** friend installs via `brew install you/tap/name`, runs it, nothing scary happens. Issues template up.

## Phase 3 — Wedge → **M3: launchable**

Order matters: G0 is the shared groundwork (candidate 5 in [architecture review](plans/2026-07-13-architecture-review.md)); G and G2 are its two consumers and can run in either order after it.

**Prompt G0 — item identity:**
> Lift selection from per-rule (`map[ruleID]bool`) to per-item: items get stable IDs `{ruleID, itemKey}`, TUI grows expandable per-item rows (toggle, size, last-used, risk badge, note), planner/executor accept item-scoped actions, `--json` carries items. No new data sources — prove it by refitting existing per-item rules (sim devices, xcode archives, TM snapshots) so per-rule selection remains the default UX and per-item is opt-in expansion.

**Prompt G — ML module:**
> Implement AI-model support per research/02 §9: HF hub via `hf`/scan-cache (dedup-aware sizes, last-used, delete via CLI), ollama list/rm, LM Studio dir detection, ComfyUI/SD surface-only. Show per-model rows w/ last-used (via G0 item identity).

**Prompt G2 — docker detection + targeting:**
> Build the docker provider per [docker usage timestamps](plans/2026-07-13-docker-usage-timestamps.md). Detection: daemon probe via docker context (Docker Desktop/OrbStack/colima all speak the same API); daemon down ⇒ rules don't apply, no error. Enumerate volumes/containers/images/networks/build cache with sizes from `system df -v`. Volume last-used = join container `.Mounts` × `.State.StartedAt/FinishedAt`; persist a usage ledger in the state dir keyed `name+CreatedAt`, merged on every scan, so history survives container removal. Classify per the research doc: protected (referenced by any container — never deletable) / caution-named (dangling + compose-labeled, never auto-selected, per-item confirm) / caution-anon (anonymous + dangling + older than N days) / safe (build cache via `unused-for`, stopped containers by age, dangling images). Image last-used from referencing containers + `LastTagTime` — never `image prune --filter until` (filters on build time). Keep-list (name/glob/compose-project) in config. Targeted per-item actions: `volume rm` preceded by tarball export to staging (VM data can't go to Finder trash — export preserves the trash-not-rm invariant; decide size cap in-session), container/image rm by ID, `builder prune --filter unused-for=`. Tests: recorded daemon JSON behind the client seam, golden per classification tier. Catalog: retire `docker-volumes.yaml` (aggregate prune) in favor of per-item rows; `docker-prune.yaml` narrows to safe tier; `docker-vm-disk.yaml` stays surface-only.

Live evidence for the classifier (2026-07-13, this machine): 3 dangling volumes were all *named, compose-labeled, active-project* data (`dakr_timescaledb_*`) — exactly what `volume prune --all` would destroy. "Dangling ≠ safe" is the whole point of the tiering.

**Prompt H — doctor + phantom:**
> `regrow doctor`: hero-bug scanners (~/.claude/debug, mediaanalysisd, .Spotlight-V100, Playwright transform cache) + phantom-space category (TM snapshots, Docker VM real-vs-logical via the G2 provider, purgeable) with explainer copy.

**M3 check:** doctor finds something dramatic on ≥1 real machine; screenshot-worthy. Docker view shows named project volumes as protected/caution with last-used dates — nothing precious is one keystroke from deletion.

## Phase 4 — Launch → **M4: public**

**Prompt I:**
> Launch kit: README hero (GIF of scan finding X GB, wedge categories first), trust section (dry-run/trash/undo/open rules/test strategy), curl|sh installer, Show HN draft (neutral 8–12 word title) + FAQ answers, r/macos + Lobsters + HelloGitHub posts. Sponsors + Ko-fi enabled.

Launch ritual (manual): Tue–Thu 9–12 ET, reply every comment first hour, follow-up release within days.

## Phase 5 — Growth (post-launch, demand-driven)

Order by issue traffic: Android/JVM rules → JetBrains/VSCode/Cursor → Rust target discovery → Linux → npx (optionalDependencies) → homebrew-core (at ~225 stars) → decide paid GUI companion ($99 Apple dev acct + notarization then).

---

## Session cadence

Each prompt ≈ one focused session. Phases 0–1 ≈ a weekend of evenings. Keep every session ending with: tests green, CHANGELOG line, one dogfood run on the real machine, Status block above ticked.
