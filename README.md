# regrow

What is on your disk, and how it comes back.

regrow is a disk cleaner for macOS, made for developers. It scans for caches, `node_modules` and Rust `target` folders, Docker leftovers, simulators and the rest of what fills a working Mac (downloaded models, other build output, agent scratch, fnm Node versions nothing references, and an Android SDK left without Android Studio are among its beta rules, below), and for each one it says what it is, how regrow would remove it, and what brings it back and at what cost. It removes nothing until you confirm the exact plan it prints, at its prompt or in advance with `--yes`. The exception is the Go build cache prune: `regrow prune go-build --yes` deletes old entries without showing a plan first, and autotrim, which deletes the same kind of entries by itself when free space is low, stays off until you turn it on. What it can, regrow moves to the Trash, where `regrow undo` finds it again.

- [docs/RULES.md](docs/RULES.md) lists every rule: what it looks at, how it removes, what it keeps, and how the space comes back.
- [docs/SCAN.md](docs/SCAN.md) says what a scan reads, which programs it runs, and what regrow writes.

## What it is, and what it is not

regrow is a catalog of rules, one YAML file each in [`rules/`](rules), and a single Go binary that measures them, plans, and runs the plan. A rule names one kind of thing (the Go build cache, an iOS simulator, a Docker volume no container uses), where it lives, its risk, the tool's own command for removing it if there is one, and its regen story: what recreates it and what that costs. Where only a tool, or a tool's own folder layout, can say what exists (Docker, `simctl`, `tmutil`, git, Ollama's manifests, the Hugging Face cache, running processes and the like), a rule runs a named query in code instead of listing paths.

It is not:

- an app uninstaller, a duplicate finder, or a tool for your documents. Rules for what may be the only copy of something (iOS backups, model libraries) or what regrow cannot judge item by item (`~/Library/Caches`, `/Library/Updates`) are surface-only: shown with their size, never deleted.
- a background cleaner. The one removal that runs by itself is autotrim of Go build cache entries unused for 24 hours, while free space is low and only until it is back at its target; a running build defers it, where `regrow prune go-build --yes` refuses. It is off until you turn it on (the app's switch, or `--autotrim` on a single `regrow tick`), and it refuses to run until a `regrow prune go-build --yes` of your own has run to completion.
- a network service. regrow's own code opens a network connection only to ask GitHub for its latest release: the command line tool once a day as it runs, the app when you choose Check for update. No telemetry, no account. A few programs it starts can reach the network on their own, and [docs/SCAN.md](docs/SCAN.md) names them.

regrow is built and tested for macOS only. The rules carry Linux paths for a later Linux build.

## Install

On a Mac with macOS 12 or later, paste this line into Terminal:

```sh
curl -fsSL https://raw.githubusercontent.com/lbagic/regrow/main/install.sh | sh
```

[`install.sh`](install.sh) downloads the latest release for your Mac's chip (Apple silicon or Intel), checks it against the release's `checksums.txt` and installs nothing when they differ, and puts `regrow` in `~/.local/bin`; it never asks for an administrator password. When `~/.local/bin` is not on your `PATH`, it adds the folder to `PATH` in the file your shell reads at login, and names that file: `~/.zprofile` for zsh, the macOS default; for bash, the first of `~/.bash_profile`, `~/.bash_login` and `~/.profile` that exists, else `~/.bash_profile`. With another shell it prints the line to add. It prints the version it installed and the command to run next. Run the same line again to update; an update whose new `regrow` does not run leaves the installed one in place.

On macOS 14 or later the same line installs `Regrow.app`, the menu bar app, in `~/Applications`, and opens it. It checks the app's archive against `checksums.txt` and checks that the app is signed by regrow's release certificate, and installs `regrow` alone, saying why, when either fails; a running Regrow quits first, as its menu's Quit does. Neither Go nor Xcode nor the developer tools are needed. The release's app is signed with a certificate of regrow's own, not Apple's: fetched by the install line it opens without a prompt, while a copy downloaded in a browser is blocked until you choose Open Anyway in System Settings ▸ Privacy & Security. On macOS 12 and 13 the line installs `regrow` alone.

### Updates

From 0.2.0 on, `regrow` keeps itself up to date. When it runs, at most once a day, it asks GitHub for the latest release in the background; when that release is newer, it downloads the archive for your Mac's chip, checks it against the release's `checksums.txt`, checks that the new `regrow` runs, puts it in place of the old one and says `regrow updated to 0.2.1; the next run uses it` on stderr. It never stops or fails the command you ran: a command that ends first waits at most 2 seconds for the check, and then only for an install already under way, which takes well under a second; offline it says nothing. `regrow update` checks at once and says what it did. Only the `regrow` the install line put in `~/.local/bin` updates itself, and only a release build: a `go install` build, a copy elsewhere, a link and a `~/.local/bin` you cannot write are left as they are. To turn the daily check off, set `REGROW_NO_AUTO_UPDATE` to any non-empty value, for example with `export REGROW_NO_AUTO_UPDATE=1` in `~/.zprofile`. A `regrow` 0.1.0 does not update itself: run the install line once more.

### From source

With Go 1.25.7 or later, the version `go.mod` names:

```sh
go install github.com/lbagic/regrow/cmd/regrow@latest
```

or, from a clone, `just install` (which runs `go install ./cmd/regrow`). Either puts `regrow` where `go install` puts programs, `~/go/bin` unless you set `GOBIN` or `GOPATH`.

The menubar app, `Regrow.app`, also builds from a clone with the Command Line Tools; no Xcode is needed:

```sh
scripts/signing-cert.sh   # once: a local code-signing identity in your login keychain
just app                  # build, sign, install into ~/Applications, launch, then run the smoke checks
```

On its first launch from `~/Applications` the app adds itself to your Login Items, once: removed in System Settings, it stays removed. It scans without the beta rules. The smoke checks raise one notification and move a folder, `~/regrow-smoke-<pid>`, to the Trash and back; on a first install they wait for you to allow Finder control and notifications, and `just app` exits 1 when one fails. `just app --no-check` installs without them. [docs/APP.md](docs/APP.md) covers what the app asks macOS for and what it runs.

### Full Disk Access

macOS hides some folders (the Trash, Spotlight's index, other apps' containers) from programs without Full Disk Access. Without it, regrow still runs: a folder it cannot read is shown as "unreadable", or its size as a lower bound ("≥ 2.1 GiB"), and an item it could not read at all is never ticked for you. To measure those folders, grant Full Disk Access to the terminal you run regrow from, or to `Regrow.app`, in System Settings ▸ Privacy & Security ▸ Full Disk Access.

## A first session

Of these, only `clean` (and the checklist, after its two confirmations) removes anything, and `undo` moves things back; the others remove nothing, and write nothing but the record of Docker volume use that a scan keeps.

```sh
regrow                   # scan: a checklist in the terminal
regrow plan              # dry run of the default selection
regrow plan sim-devices  # dry run of what you name
regrow clean             # the same plan, a y/N prompt, then it runs
regrow history           # what earlier runs did
regrow undo              # put back the newest run's moves still in the Trash or staging
regrow doctor            # runaway bugs, what keeps filling the disk, phantom space
```

1. **Scan.** `regrow` (or `regrow scan`) measures every rule but the beta ones and opens a checklist, grouped and largest first. Each row shows its size, its risk, and the command that would remove it or its regen story; the line under the list explains the row under the cursor: for a rule, what brings it back and at what cost (or its error, or that regrow never deletes it); for an item, its id and what brings it back. `→` opens a rule into its items. Safe rules that found something without an error start ticked ([docs/RULES.md](docs/RULES.md) says what happens to items that could not be read). Keys: `↑`/`↓` move, `space` ticks a rule, or an item of a rule that acts item by item, `→`/`←` open and close a rule, `enter` shows the plan, `q` quits. Piped (`regrow scan | less`), it prints the same findings as text, with each item's id.
2. **Plan.** `enter` in the checklist, or `regrow plan [id ...]`, shows the exact commands the run would execute, one per line, with what is skipped and why, and what it would reclaim (the space it frees as it runs, and what it moves to the Trash, freed once the Trash is emptied), with those two parts as totals of their own, and, when the plan removes a Docker volume, its export, a tarball in regrow staging that holds that space until you delete it and that the reclaim figure leaves out. Nothing has run yet.
3. **Clean.** In the checklist, `x` on the plan screen and then `y` runs what is ticked. From the shell, `regrow clean [id ...]` prints the plan and asks `Proceed? [y/N]`; `--yes` skips the question, for scripts. Without ids, `clean` takes the default selection, the same rules the checklist starts with ticked. A caution rule runs only when you name or tick it. A command that needs administrator rights, of which the catalog has two (the Spotlight index rebuild, `mdutil -E /`, and the aerial wallpaper videos' delete, both caution rules), runs through `sudo`, which asks for your password. When the run ends, regrow prints the run's id and where the space went: freed now, in the Trash, in regrow staging, and in the tarballs of removed Docker volumes, which stay in staging until you delete them.
4. **History.** `regrow history` lists every run with its id, its time, the size its finished actions cleaned, how many actions finished, failed and were undone, how many of its Trash and staging moves can still be restored, and how many are gone from the Trash or staging.
5. **Undo.** `regrow undo` moves back where they were the Trash and staging moves of the newest run with a copy still on disk; `regrow undo <run-id>` picks a run, and refuses one whose copies are all gone. It cannot restore what a tool's own command deleted, what `regrow prune go-build` or autotrim deleted, nor anything emptied from the Trash or removed from staging since, and it says how many moves it left for that reason.
6. **Doctor.** `regrow doctor` scans only the rules that watch for a known runaway bug (a Claude Code debug-log loop, the mediaanalysisd leak, a bloated Spotlight index, the Playwright cache) and the phantom-space rules, checks the settings that keep refilling the disk (aerial wallpaper downloads on macOS 15 and earlier, Docker's build-cache limit and memory cap, Go builds in worktrees without `-trimpath`), and explains phantom space. It prints the fix for each and changes nothing.
7. **Aerials.** On macOS 15 and earlier, `regrow aerials` says whether macOS is downloading aerial wallpaper videos. It downloads for each aerial picked for the wallpaper or the screen saver, at least 4 videos of about 0.5 GB per category and more every day or two, and a still wallpaper chosen later leaves the picks in place. `regrow aerials off` records the picks, clears them and stops the downloader, asking for your password once; `regrow aerials on` puts them back. `status` and `off` warn when an aerial is still set as your wallpaper or screen saver, with the link that opens its pane in System Settings, where picking an aerial picks it again. Once nothing is picked, `regrow clean aerial-wallpapers` deletes the videos and the downloader's temp copies of them as root, asking for your password, with no Trash and no undo; while anything is picked, the videos are only shown, since they would come back. On macOS 26 and later these commands change nothing and the videos are only shown: the picks were not measured there.

Ids are rule ids (`sim-devices`) or item ids (`sim-devices/AAA-111`), as the scan lists them. `--beta-rules` adds the rules still in staged rollout to any command.

## Commands

| Command | What it does |
|---|---|
| `regrow [scan] [--json]` | Scan: the checklist in a terminal, a plain listing when piped; `--json` prints the scan as JSON lines ([docs/ENGINE.md](docs/ENGINE.md)). |
| `regrow plan [id ...] [--json]` | Dry run: the exact commands a clean would run. |
| `regrow clean [id ...] [--yes]` | Show the plan, ask, run it. |
| `regrow undo [run-id]` | Restore the Trash and staging moves of the newest run with a copy still on disk, or of the run named. |
| `regrow history [--json]` | Past runs, from the oplog. |
| `regrow doctor [--json]` | Runaway bugs, causes to fix, phantom space. Read-only. |
| `regrow tick [--autotrim] [--json]` | One sample of free space, purgeable space and swap, with a days-to-full forecast and alerts; `--autotrim` also prunes the Go build cache when free space is low. |
| `regrow prune go-build [--yes] [--json]` | Delete Go build cache entries not used for 24 hours, oldest first, until the cache is down to 15 GiB, and nothing when it already is; a dry run without `--yes`. |
| `regrow rules [--json]` | List the catalog. |
| `regrow engine` | JSON lines on stdin and stdout, for the menubar app ([docs/ENGINE.md](docs/ENGINE.md)). |
| `regrow version`, `regrow help` | Print the version, or this list with the flags. |

Flags go before ids: `regrow clean --yes go-build-cache`. `--rules-dir DIR` loads rules from a folder instead of the built-in catalog.

## The safety model

**Dry run by default.** `plan`, `doctor`, `rules`, `history`, `tick` without `--autotrim`, and a scan you only look at remove nothing. Autotrim aside, a removal needs a confirmed plan: `y` at the `clean` prompt (or `--yes`), `x` then `y` in the checklist, or the tick box and button on the app's confirm step. `regrow prune go-build` is a dry run until `--yes`.

**The Trash, not `rm`.** A rule with no command of its own moves each item to the Trash through Finder, so Finder's Put Back works and `regrow undo` can move it back. When Finder fails (in an SSH session, say, or the app without permission to control Finder), the item is renamed into regrow staging instead, `~/.local/state/regrow/staging/<run-id>/`, which `regrow undo` also restores and emptying the Trash does not free. An item on another volume cannot be renamed there: its action fails and the item stays where it was.

**The tool's own command, where there is one.** Where a tool keeps its own books, regrow runs the tool's cleanup command rather than deleting its files: `go clean -cache`, `docker image rm`, `xcrun simctl delete`, `ollama rm`, `fnm uninstall`, `tmutil deletelocalsnapshots`. These delete for good: `regrow undo` cannot bring them back, and the rule's regen story says what will. Each plan line says "undo restores" or "no undo". Three removals deserve a closer look, and [docs/RULES.md](docs/RULES.md) spells each out:

- a Docker volume is exported to a tarball in regrow staging before `docker volume rm`, and a volume bigger than the export cap (10 GiB unless you set it) is never offered;
- `regrow prune go-build --yes` deletes old Go build cache entries directly, with no Trash and no undo;
- `regrow clean aerial-wallpapers` deletes the aerial wallpaper videos and the downloader's temp copies of them as root, with no Trash and no undo, and only while macOS has no aerial picked; the run's journal keeps how many videos it deleted.

**Undo.** `regrow undo` restores Trash and staging moves from the oplog. It refuses to overwrite anything that has reappeared at the original path, and it says how many actions of the run it cannot restore.

**Surface-only rules never delete.** iOS backups, model libraries, `~/Library/Caches`, `/Library/Updates`, the aerial wallpaper videos (while macOS has an aerial picked to download them for or the picks cannot be read, and always on macOS 26 and later), a macOS update Software Update downloaded, an upgrade bundle left for an older macOS than the one running, Homebrew casks whose app is gone and whose uninstall asks for a password, Chrome's on-device AI model, the Docker VM disk, APFS purgeable space, and the Docker volumes and agent sessions regrow keeps back are shown so you know where the space is. The planner refuses them even when you name them, and refuses to remove any folder that holds one.

**Beta rules stay out until asked for.** A new rule ships with `beta: true` and joins a scan only with `--beta-rules`, until it has run on real machines.

**The oplog comes first.** Every action is written to `~/.local/state/regrow/oplog.jsonl`, and synced to disk, before it runs; its end is written when it finishes or fails. A Trash move's line records where the item landed, and that record is what `regrow undo` uses. `regrow history` reads the same file.

**Path guards.** Every Trash move, and every command run on one item's path, is refused when the path is empty or relative, is `/`, a folder directly under `/` (such as `/Applications` or `/Users`), your home folder, or a volume's root under `/Volumes`. The planner checks, and the mover checks again right before the move.

**Checked again right before.** Rules whose verdict can change after the scan judge each item again right before they remove it: an agent session that started working again, a worktree that gained a change, a build folder whose project is in use, an npx folder a process started using, a download whose app was removed, a Node version something started naming, aerial videos macOS has an aerial picked for again. A refusal leaves the item in place and fails that one action.

**Small refusals.** `clean` refuses an id that matches nothing in the scan rather than run less than you meant. A rule whose command acts on everything at once cannot be run for some of its items. "Empty Trash" is never ticked for you, and when you pick it, it runs before the plan's Trash moves, so those stay restorable.

## What regrow never does

- It never removes anything you did not confirm, except autotrim, which you turn on, which unlocks only after a `regrow prune go-build --yes` of your own that runs to completion, and which removes only Go build cache entries unused for 24 hours while free space is low.
- It never deletes a surface-only item, or a folder that holds one.
- It never empties the Trash unless you pick "Empty Trash".
- It never runs `rm`. Deletion goes through the Trash, a tool's own command, the Go build cache prune's `find … -delete`, or the aerial videos' `find … -delete` as root.
- It never changes a system or tool setting, except that the app adds itself to your Login Items once, and `regrow aerials off` and `on`, which you run, clear and restore macOS's aerial picks. `regrow doctor` prints the fix and leaves it to you.
- It never sends what it learns about your disk off the machine.

## Where regrow keeps its state

| Path | What |
|---|---|
| `~/.local/state/regrow/oplog.jsonl` | the journal of every action, behind `undo` and `history` |
| `~/.local/state/regrow/staging/<run-id>/` | items moved here when Finder failed, and Docker volume exports |
| `~/.local/state/regrow/docker-volumes.json` | the latest use of each Docker volume that a scan saw a container make, kept by every scan that reaches Docker |
| `~/.local/state/regrow/headroom.jsonl` | free-space samples from `regrow tick` and the app, 30 days |
| `~/.local/state/regrow/prune.state` | the lock and the build-deferral clock of the Go build cache prune |
| `~/.local/state/regrow/aerials.json` | the aerial picks `regrow aerials off` cleared, which `regrow aerials on` puts back |
| `~/.local/state/regrow/aerials.json.lock` | the lock that keeps a second `regrow aerials off` or `on` out while one runs |
| `/var/run/regrow-aerials.lock` | an empty file, the lock the root step of `regrow aerials off` or `on` holds for as long as it runs |
| `~/.config/regrow/config.yaml` | optional settings (below); only the app's Rules section writes it, its `rules:` key alone |

`XDG_STATE_HOME` moves `~/.local/state/regrow`, and `XDG_CONFIG_HOME` moves `~/.config/regrow`. regrow never empties its staging folder: what lands there stays until you remove it. [docs/SCAN.md](docs/SCAN.md) lists every file regrow writes; the app's own files are in [docs/APP.md](docs/APP.md).

### Settings

The settings file is optional; a key regrow does not know is an error, not a silent default.

```yaml
docker:
  keep:                       # Docker objects regrow never offers
    - "postgres-*"            # a name glob: volumes, stopped containers, image repositories
    - project: billing        # every volume and container of a compose project
  volume_export_cap: 20GiB    # volumes bigger than this are never offered (default 10GiB)
rules:
  off: [npm-cache]            # rules regrow does not look at: no scan, plan, clean, tick or doctor runs them
  beta: true                  # run the rules still in staged rollout, as --beta-rules does
```

A command that names a rule turned off, such as `regrow clean npm-cache`, refuses and says where it is off. `regrow rules` lists the catalog, the beta rules only with `--beta-rules` or `beta: true`, an off one marked "(off)"; an id under `off` that no rule has is named on stderr. A file that does not load, holds more than one YAML document, or is reached through a link to nothing (the file's or that of any folder above it), stops every command that runs rules; `regrow rules` still lists them and then says why, `regrow undo` and `regrow history` run regardless, and the app keeps watching free space but runs no rule until it loads.

## Working on regrow

```sh
just            # go build, go vet and go test: the bar every change keeps green
just golden     # refresh the per-rule golden snapshots after a deliberate rule change
go test ./internal/rulesdoc -update   # regenerate docs/RULES.md after a rule change
```

Adding a cleaning target is usually a YAML file in `rules/` with a `fixture:` block, which the golden test scans in a fake home. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) holds the structure, the safety invariants and the decisions log; [docs/PRODUCT.md](docs/PRODUCT.md) the why; [docs/PLAN.md](docs/PLAN.md) the build order; [CONTEXT.md](CONTEXT.md) the words the code uses.

## Other tools (surveyed July 2026)

- [npkill](https://github.com/voidcosmos/npkill) finds and removes `node_modules`, and is the best at that one job.
- [mac-cleanup-py](https://github.com/mac-cleanup/mac-cleanup-py) has the broadest set of modules; its dry run is a flag, not the default.
- [Mole](https://github.com/tw93/Mole) is the most popular terminal cleaner for macOS.
- [DevCleaner](https://github.com/vashpan/xcode-dev-cleaner) is the best GUI for Xcode's files.

## License

MIT.
