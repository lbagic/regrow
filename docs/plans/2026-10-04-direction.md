# Direction after the blind reviews

**Status:** proposed — D1 decided on evidence; D2–D4 await the owner. Prototype on branch `proto/web-serve`.

Four blind agents looked at this machine and at regrow without reading the repo's code, docs or rule catalog: a disk-usage inspector, a residue/growth inspector, a product reviewer using the binary (dry-run only), and a first-principles product designer. Their raw reports name private projects and paths, so they live outside the repo (`~/.claude/handoffs/regrow-blind-review-2026-10-04/`). This doc keeps the generic findings and the decisions.

## The problem, measured

The owner "runs out of memory every now and then". On this machine that is disk, and the two are coupled:

- Free space on the 460 GiB Data volume: 112.8 GB (09-28, from DiagnosticReports) → 31 GiB (10-04 10:08) → 11 GiB (10-04 11:05).
- The last drop was swap: a runaway 28 GB process pushed swap from 4 to 23 GiB in under an hour, and swap files live on the same disk. When the disk is near full, swap cannot grow and macOS reports "out of application memory". Low disk and memory pressure feed each other, so free-space *headroom* is the thing to protect.

Where the space goes, ranked by how much it explains the recurring crunch:

| # | Consumer | Size | Why it recurs |
|---|---|---|---|
| 1 | Go build cache | 55 GiB, every byte < 5 days old | Bursts of builds across many git worktrees. Go keys cache entries by absolute package dir unless `-trimpath` is set, so each worktree recompiles the main module (24 copies of one 437 MB package seen). Go trims entries after 5 days unused, so the cache oscillates instead of growing forever. |
| 2 | Swap | 4 → 23 GiB today | Memory pressure from runaway processes; directly converts RAM pressure into disk loss. |
| 3 | Aerial wallpaper videos | 44.3 GiB | Shuffle downloads everything (79 files in 2 hours on 09-26, ~1/day since). Reviewers disagree on whether macOS treats it as purgeable: purgeable total (43.8) matches it and idleassetsd is a purgeable provider, but the files carry no per-file purgeable flag. Fix is a setting, not a delete. |
| 4 | Agent scratch in `/private/tmp/claude-*` | 14.6 GiB, 768k files | Accumulates between reboots; nothing else clears it. |
| 5 | One-off residue | ~45 GiB | Simulator runtime with no Xcode (12), Android SDK with no Studio (11.2), stale macOS installer data (10.6), data of an uninstalled editor (5.6–7.2), browser on-device model (4), 15 Node versions (4.3). |
| 6 | Worktrees and project build output | ~15 GiB | One agent-heavy repo has 243 registered worktrees (78 merged); `node_modules`, `.next` per worktree. |
| 7 | Docker | ~9–13 GB reclaimable | Unused (non-dangling) images, unused volumes, old build cache. |

The takeaway: most of the space *regrows*. A one-off clean does not hold; the product has to watch headroom and act on the regrowing classes, and separately surface the one-off residue nobody owns.

## What the product review found

Right: per-row sizes are accurate, the regeneration stories are the best part, and the safety core (trash, oplog, undo) is sound.

Bugs, each reproduced by the reviewer:

- **B1 — scan hangs forever.** Without Full Disk Access, `open(2)` on `~/Library/Containers/com.apple.mediaanalysisd/Data/Library/Caches` blocks indefinitely (a plain `ls` blocks too; the parent dir opens fine). The TUI spinner never ends — this is the "not loading" report. *Fixed on the prototype branch:* a 3 s open probe turns it into an error row.
- **B2 — the headline total double-counts.** `~/Library/Caches` (69.8) contains go-build, Yarn, Playwright, Homebrew, pip (62.6); `Docker.raw` contains the docker rows; APFS purgeable ≈ aerials. Claimed 279.8 GiB vs ~164 GiB distinct vs ~67 GiB actually freed today.
- **B3 — permission failures render as `0 B`** (Trash, Spotlight); `doctor` prints them as healthy.
- **B4 — "last used" is the directory mtime**, so a cache written yesterday can read "unused 225d".
- **B5 — Empty Trash is ordered mid-plan**, so it would permanently delete what the same run just trashed. Safety bug; also the "Trash first, undo restores" footer is false for ~113 of 118 GiB (native commands).
- **B6 — `node_modules` discovery depth cap** misses 26 of 58 dirs (3.7 GiB).
- **B7 — docker build cache** claims 1.6 GiB where `docker system df` says 0 reclaimable.
- **B8 — `--help`** prints "Usage of scan:" with no subcommand list; CLI `plan` and the TUI preselect different things.

## Decisions

**D1 — No rewrite. Keep Go.** (decided, evidence)
The scan is bound by kernel file-metadata work, not the language: C `du` 9.6 s vs sequential Go 11.6 s on the same 411k-entry tree; the system/user CPU split of a full scan is 35 s / 6 s. Parallel walking within a tree is the lever: 4 workers → 3.2 s (8–32 no better). `getattrlistbulk` with 8 threads measured 1.9–2.7 s on that tree, a further ~25% — worth having later, not a reason to switch languages. One walk instantiates ~379k vnodes, more than the kernel keeps cached, which is why a second scan is no faster.

**D2 — The face becomes a local web UI served by the Go binary; the TUI stops being primary.** (proposed)
The prototype shows it works: first rows render 10 ms after open, the full scan streams in 17–20 s, totals stay honest by subtracting nested rows, and the loopback hardening (exact Host, per-run token, same-origin POST) rejects what it should. Later the same UI goes inside a small menubar shell (SwiftUI + WKWebView, or Tauri) that holds Full Disk Access as a stable .app identity and can send alerts — a browser tab cannot. Electron is ruled out on footprint. Trap to remember: a bare binary under launchd loses its TCC grant on every rebuild (ad-hoc signature changes).

**D3 — The product loop shifts from "cleaner you run" to "never run out".** (proposed)
Watch headroom (free space and swap) and forecast days-to-full; alert before the crunch. Autopilot only the self-regenerating classes under explicit per-rule policy (e.g. go-build entries untouched > 48 h, scratch dirs of dead agent sessions), opt-in after the owner has run the rule by hand once. Everything else lands in a short review queue where every byte has an owner (app, repo/worktree, toolchain, container engine, OS, user) and residue is "owner gone or done".

**D4 — Coverage priority follows the table above, not the old catalog order.** (proposed)
New coverage, highest value first: worktrees (merged by ancestry or PR + clean + idle; delete only ignored build dirs when unmerged), agent scratch dirs with no live session, project build output in stale projects (`.next`, `dist`, `.turbo`, `.venv`, `target`), unused Docker images, toolchains nothing references (Node versions — guard versions hard-coded in LaunchAgents — Android NDKs, simulator runtimes without Xcode), app leftovers by bundle id (archive, don't delete), installers in Downloads, npx cache, stale macOS installer data. Plus a "fix the cause" channel: `-trimpath` for worktree-heavy Go repos, aerial shuffle off, Docker build-cache limit.

## Plan

1. **Stop the bleeding (owner, manual, now).** Kill runaway processes; `go clean -cache` (55 GiB, will regrow); try `GOFLAGS=-trimpath` in the worktree-heavy repos and watch whether worktrees start sharing cache; switch the wallpaper/screensaver off aerial shuffle; prune merged worktrees; reboot clears agent scratch. Expected: roughly +80–100 GiB of headroom on top of whatever the runaway's swap gives back.
2. **Correctness and trust** (fix B1–B8). Merge the open probe, the parallel walker and `ScanStream` from the prototype; dedupe nested paths in totals and show macOS-managed space outside them; explicit "unreadable — grant Full Disk Access" state; last-used from newest file mtime; Empty Trash always last; split the total into *frees now / after Trash is emptied / macOS-managed / regrows within N days*.
3. **Web UI as primary** (`regrow ui`): the prototype hardened, plus the execute path behind the existing double confirm and the executor. Retire or freeze the TUI.
4. **Coverage** per D4, each new rule with its golden fixture.
5. **Watch and autopilot**: a launchd agent sampling free space and swap, days-to-full forecast, notifications; opt-in autopilot rules. Then the menubar shell.

Performance work rides along with 2–3: walk each subtree once (container rules subtract children instead of re-walking them), stream cheapest-first, later `getattrlistbulk` and a persistent index refreshed by FSEvents.

## Prototype verdict (`proto/web-serve`)

Question: does a localhost web UI feel better than the TUI and is it fast enough? Answer: yes on speed and information density — rows stream in as rules finish instead of a 28 s spinner, and the honest per-row "unique" size makes double counting visible. It deliberately has no execute endpoint, so the execute flow in a browser is still unproven. Run it with `just proto` (or `go run ./cmd/regrow proto-serve`).

## Open questions for the owner

1. D2: local web UI now and a menubar shell later — or go straight to the menubar app?
2. D3: is regrow allowed to trim go-build entries untouched for 48 h without asking each time?
3. Scope: is regrow a personal tool first (fix this machine's crunch) or still the launch product of PLAN.md Phase 4? The answer reorders everything after step 2.
4. Grant Full Disk Access to the terminal so scans can see Trash, Mail, Messages and the Spotlight index (~20 GiB currently invisible)?
