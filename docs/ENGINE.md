# Engine protocol

`regrow engine` lets a shell app (the menubar app) watch free space, scan, plan and execute without a terminal. It reads requests on stdin and writes events on stdout, one JSON object per line. The code lives in `internal/protocol`; this document is the contract.

```sh
regrow engine [--rules-dir DIR] [--beta-rules]
```

Protocol version: **1**.

## Framing

- Every line is one UTF-8 JSON object ending in `\n`.
- A request line may be up to 1 MiB (1,048,576 bytes, newline excluded). A longer line is discarded up to its newline and answered with an `error` event (`line_too_long`) that has no `re`. The next line is read normally.
- Blank request lines are ignored. Unknown request fields are ignored.
- Every event carries `event`, its name, and `re`, the `id` of the request it answers. Only `hello` and errors about a line without a string `id` have no `re`.
- Events reach stdout in the order the engine produced them, written by a single writer. A peer that stops reading never stalls a scan, an execute or the handling of later requests: events queue until it reads again.
- Peers ignore event names and fields they do not know. New events and fields can appear within protocol version 1; a change that breaks an existing field bumps the version.
- stderr is never protocol. It carries text for people: the reason when the engine exits with status 1, and a crash report if it crashes. Steward commands' output is captured, not passed through. The peer drains stderr or points it at a file: a full pipe would block the engine.

## Lifecycle

1. The engine starts and writes `hello` before it reads anything.
2. The peer sends requests. Each request gets exactly one terminal event: `done` or `error`, with `re` set to the request's `id`.
3. Only one **scan, execute, tick or aerials switch** is in flight at a time. Another one sent meanwhile is refused with `error{code:"busy"}` and changes nothing. `plan`, `cancel`, `history`, `aerials`, `rules`, `set_rules` and `updates` are answered at any time.
4. When stdin ends, or the process gets SIGINT or SIGTERM, the engine shuts down: a scan in flight is canceled, an execute finishes the action it is running, journals it and stops there, a tick runs to its end, and an aerials switch stops as a cancel stops it (below): a root step the password has already started may still run to its end. It waits for the execute, the tick or the aerials switch however long it takes, and for a canceled scan up to 5 s from the start of the shutdown; for a `history` or `updates` being read it waits as long, or until the execute, the tick or the aerials switch ends if that is later. Each that ends in that time writes its terminal event; a scan still going, a `cancel` waiting on it, or a `history` or `updates` still going gets none. A peer that stopped reading is given 5 s more to read what is queued, and what is left then is dropped. Then the engine exits 0. A peer that cannot wait out a hung steward command sends SIGTERM twice: the first signal asks for the clean stop, the second kills the engine at once.

Request ids are the peer's own; the engine echoes them and does not check that they are unique.

## Requests

| Request | Fields | Answer |
|---|---|---|
| `scan` | `id` | `headroom`, `start`, one `finding` per rule, `summary`, then `done` |
| `plan` | `id`, `scan_id`, `select` (optional) | `plan`, then `done`; or `error` |
| `execute` | `id`, `plan_id` | one `journal` per oplog line, then `done`; or `error` |
| `cancel` | `id`, `target` | `done`, once `target` has ended |
| `tick` | `id`, `autotrim` (optional, default `false`) | `tick`, then `done`; or `error` |
| `history` | `id` | `history`, then `done`; or `error` |
| `aerials` | `id` | `aerials`, then `done`; or `error` |
| `aerials_switch` | `id`, `to` (`off` or `on`) | `aerials`, then `done`; or `aerials`, then `error`; or `error` alone when it is refused: a `to` that is neither, busy, or an engine that cannot change the picks |
| `rules` | `id` | `rules`, then `done`; or `error` |
| `set_rules` | `id`, and `rule` with `off`, or `beta` | `rules`, then `done`; or `error` |
| `updates` | `id` | `updates`, then `done`; or `error` |

### scan

```json
{"type":"scan","id":"s1"}
```

Starting a scan makes it the engine's current scan and drops the previous scan's findings and every plan built from them, executed or not.

| Event | Fields |
|---|---|
| `headroom` | the free-space sample's own fields on the event line: `at`; `total` and `free` of the data volume (`free` is what df shows); `purgeable`; `swap_used` |
| `start` | `scan_id`: names this scan in `plan`; `rules`: how many `finding` events follow |
| `finding` | `scan_id`; `index`: the rule's position in the catalog; `took_ms`: the rule's own scan time; `bucket`: the total the rule's bytes are counted in; `select`: the atoms that tick this row, absent when it cannot be ticked; `finding`: the Finding (below) |
| `summary` | `scan_id`; `totals`: the Totals of every item; `exclusive`: item id → bytes the item frees on its own, nested items excluded |
| `done` | `elapsed_ms`; `canceled`: `true` when the scan was canceled |

`finding` events arrive in completion order, not catalog order. A canceled scan sends no further `finding` and no `summary`, and cannot be planned.

A Finding is `{"rule": Rule, "items": [Item], "error": "…"}`; `error` is set only for a rule-level failure (a tool query that failed or gave no answer in time, an unknown query); a tool that is not installed is no error, the rule just finds nothing. An item with `partial` and no `path` stands for targets the scan could not enumerate, a folder behind a glob or a discover root, and its label says which; no plan acts on it. Rule is the catalog rule as `regrow rules --json` prints it. An Item is:

| Field | |
|---|---|
| `path` | filesystem path; absent for tool items such as a docker image |
| `label` | display name |
| `key` | stable identity within the rule; `<rule id>/<key>` is the item id `select` takes |
| `arg` | the tool's handle, substituted into a per-item steward command |
| `bytes` | physical bytes, nested items included |
| `last_used` | RFC 3339 time, absent when unknown |
| `partial` | `true` when `bytes` and `last_used` are lower bounds: something was unreadable or blocked. With `bytes` 0 nothing could be measured: the item is unreadable, its size unknown |

Totals, in `summary` and `plan`, split bytes by when they become free space:

```json
{"frees_now":0,"after_trash":0,"exported":0,"shown_only":0,"macos_managed":0,"partial":0}
```

`frees_now` is freed as steward commands run; `after_trash` only once the Trash is emptied; `exported` is a docker volume removed after its export, whose tarball keeps about as much room in regrow's staging directory until it is deleted; `shown_only` is surface-only and never deleted; `macos_managed` is phantom space macOS reclaims itself. `partial` counts items whose bytes are lower bounds.

A finding's `bucket` is the name of one of the five: `frees_now`, `after_trash`, `exported`, `shown_only` or `macos_managed`. It is always present, and it is the bucket the `summary` counted the rule's items in, so a peer never works it out from the rule.

A finding's `select` lists the atoms that tick its row, in item order. A peer that lets the owner pick sends only atoms it was given here:

- absent when the rule is surface-only, found nothing, or failed: nothing to tick;
- item ids, each once, when the rule acts per item (a Trash move, or a steward command that names its item): each can be picked on its own. Unreadable items are left out, so one can never be picked, and a rule with nothing read has no `select`;
- the rule's id alone, when a steward command acts on the whole rule and every item was read: the row is all or nothing;
- absent when such a command would take an unreadable item along.

### plan

```json
{"type":"plan","id":"p1","scan_id":"…","select":["go-build-cache","sim-devices/AAA-111"]}
```

- `select` absent or `null`: the default selection, every safe rule that found something without an error, leaving out unreadable items. A rule with some unreadable items is selected item by item when it acts per item, and left out when its steward command acts on the whole rule.
- `select: []`: an empty plan.
- Otherwise rule ids and item ids, as `regrow clean` takes them.

| Event | Fields |
|---|---|
| `plan` | `plan_id`; `expires_at`; `select`; `plan`: `{actions, skipped}`; `totals` (`frees_now`, `after_trash` and `exported` are filled) |
| `done` | — |

The event's `select` is the atoms the plan was built from, sorted: the request's own, or the default selection when the request sent none, which is how a peer learns what starts ticked. The default selection names a rule read in full by its rule id, also where the finding's `select` lists item ids: the rule id stands for all of them. `expires_at` is an RFC 3339 time, the wall-clock moment after which an `execute` of this plan answers `plan_expired`; an execute at that moment still runs.

An action is `{rule_id, item_key, kind, command, pre_action, path, bytes, empties_trash, sudo, includes}`: `kind` is `native` (a steward command, no undo) or `trash` (a Finder move that `regrow undo` restores), `command` is the exact argv, and action bytes never overlap, so they sum to the plan's total. `includes` is `[{id, bytes}]`, absent when empty: the scanned items inside the action's target that no action of this plan deletes on its own, such as an unselected `node_modules` inside a worktree being removed. They are deleted with the target, each `id` is an item id as `select` takes them, and the action's `bytes` already counts theirs, so a peer shows them and never adds them. A skip is `{rule_id, item_key, reason}`. Two kinds of action are never planned here, only skipped, one skip for the rule or one per item when only some of its items were picked. `regrow plan` and `regrow clean` in a terminal plan both as actions.

- An action that would need administrator rights: the reason names the Terminal command, for example ``needs administrator rights — run it in Terminal: `regrow clean spotlight-index` ``. The command starts with `regrow` when that name on the engine's PATH is the engine's own binary. Otherwise it starts with the binary's absolute path, quoted for the shell where it needs it, as for an engine inside an app bundle: ``run it in Terminal: `/Users/dev/Applications/Regrow.app/Contents/MacOS/regrow-engine clean spotlight-index` ``.
- An action whose program, the first word of the rule's `native_command`, is not on the engine's PATH: the reason is `` `go` is not on the PATH regrow was started with ``. Run, the action could only fail; skipped, it is a state the owner sees before confirming. The program is looked up before the rights are, so a missing program reads as missing.

A plan id:

- is executable once, for 10 minutes of wall-clock time, sleep included: until the plan event's `expires_at`;
- dies when a newer scan starts, even one later canceled;
- dies when an execute of its scan starts running: that run changes what the scan measured, so every plan of the scan is dropped and planning from it again answers `scan_spent`. An execute refused as `busy` spends nothing, and one refused later but before it ran (`plan_expired`, an `execute_failed` with no `result`) spends only its own plan id;
- is the oplog run id its execution journals under, so `regrow history` and `regrow undo <plan_id>` find the run;
- carries 128 random bits, so it cannot be guessed.

Errors: `bad_request` (no `scan_id`), `unknown_scan`, `scan_running`, `scan_canceled` (scan again), `scan_superseded` (a newer scan replaced it), `scan_spent` (an execute ran against it; scan again), `unmatched` (a selector addressed nothing in the scan; the event lists them in `unmatched` and no plan id is issued, as `regrow clean` refuses).

### execute

```json
{"type":"execute","id":"x1","plan_id":"…"}
```

The engine executes on request: the peer's confirmation is the per-run opt-in. Actions run in plan order, one at a time. Every oplog line is synced to disk before its `journal` event is written, and an action's `start` line is synced before the action runs.

| Event | Fields |
|---|---|
| `journal` | `entry`: the oplog line, `{time, run, seq, event, rule_id, item_key, kind, command, path, bytes, receipt, error}`; `event` is `start`, `done` or `fail`; a done Trash move carries `receipt: {original, to, method, ino}`, where `ino` is the inode of the item at `to`, read right after the move, and is absent when that read failed and in lines written before it was recorded |
| `done` | `result`; `canceled`: `true` when a cancel or shutdown stopped the run before every action was attempted; `staging_dir`: the run's folder in the staging directory, where its staged moves and volume exports are |

`result` is `{run_id, done, failed, bytes, trash_bytes, staged_bytes, exported_bytes, failures, skipped, pruned, stopped}`. `bytes` counts successful actions; `trash_bytes` is the part now in the Trash, freed once it is emptied; `staged_bytes` the part moved into regrow's staging directory because Finder was unavailable, which emptying the Trash never frees; `exported_bytes` the part that is a docker volume removed after its export, whose tarball stays in the run's folder in the staging directory, about as large as the volume was, until someone deletes it, and which `undo` does not restore. What is left of `bytes` after the three parts is what steward commands freed as they ran, and a docker volume is not in it. `pruned` is `{files, bytes}` deleted by prune actions; a plan from `plan` has none, only a tick's autotrim prunes. One failed action does not stop the run.

A steward command runs with stdin from `/dev/null` and its stdout and stderr captured. When it fails, the last 4 KiB of that output ends the `fail` line's `error`. The docker export that precedes a volume removal is captured the same way, except that its stdout is the tarball.

An action whose rule declares a `pre_action` runs that hook first: the docker volume export before a volume removal, the agent-session recheck before a session's scratch goes to the Trash, the worktree recheck before `git worktree remove`, the build-output recheck before a build folder goes to the Trash. A hook that fails or refuses fails the action: its `fail` line carries the reason, and nothing is moved or removed.

A Trash move that has not finished after 60 s fails with a reason, and the run goes on. The limit covers the move alone, not a hook before it. A folder macOS blocks without Full Disk Access can hold a move forever, and no cancel reaches it. If the abandoned move completes later, the item is in the Trash, where Finder's Put Back restores it, or in regrow staging if Finder failed and the fallback ran; `regrow undo` has no receipt for either.

Errors: `bad_request` (no `plan_id`); `busy`; `unknown_plan` (never issued, already executed, or dropped when an execute or a newer scan started); `plan_expired`; `execute_failed`, either before the run starts (an unreadable config file, a journal that cannot be opened; nothing ran) or when the journal fails mid-run (nothing more runs, and the event carries the `result` of what did). `bad_request` and `busy` refuse before the plan id is looked up and leave it executable. Past them, the first execute that names a plan id spends that id, whatever the outcome.

### cancel

```json
{"type":"cancel","id":"c1","target":"s1"}
```

`target` is the `id` of a scan, execute, tick or aerials switch. A scan stops at once. A tick is never cut short. An aerials switch ends the `osascript` that shows macOS's password prompt and runs the root step; whether that closes a prompt still open is not measured. Stopped while `osascript` runs, with the prompt open or the root step running, which its end cannot tell apart, it ends with `error` (`aerials_failed`) saying it was stopped and may still finish, and keeps the record of the picks: a root step the password has already started may still run to its end, so the status it carries may be from before it. That step holds the root lock for as long as any of its processes runs, so the root step of the next switch, or of an `off` or `on` in a terminal, waits for it once the password is given, up to 30 s; one still held then ends that step with exit status 1, changing nothing, and the switch with `error` (`aerials_failed`) saying the root step changed nothing and could not take the lock. Stopped before `osascript` started, its reads of the picks included, it ends with `error` saying nothing changed; stopped as or after `osascript` ended, it ends as the step did. An execute finishes the action it is running, journals it, and runs nothing after it. The target then ends with its terminal event, and only after that does the cancel get its own `done`, so a `scan` sent after the cancel's `done` is never refused as busy. A target that is not in flight has nothing left to do: the cancel gets `done` at once.

The target's `done` says whether the cancel cut it short. Peers read `canceled`; an execute's `result.stopped` carries the same value. A cancel that lands as the target finishes (during an execute's last action, or as a scan wraps up after its last finding) can come too late to change anything: the target then ends `canceled:false` with its full results, and a scan that ends that way can be planned.

### tick

```json
{"type":"tick","id":"t1","autotrim":true}
```

One pass of the watch loop, as `regrow tick` runs it: a free-space sample appended to the headroom history, the forecast, the alerts that crossed, and with `autotrim` a prune of the Go build cache when headroom is low. Autotrim is refused until the oplog holds one completed `regrow prune go-build --yes`.

| Event | Fields |
|---|---|
| `tick` | the sample's fields as in `headroom`, plus `days_to_full` (absent without a forecast), `no_forecast` (why there is none), `alerts` (`[{kind, message, band, acute, top_process}]`, absent when none crossed), `pruned` (what autotrim did or why it did not run: `{rule_id, skipped, run, files, bytes, free_before, free_after, error}`), `history_unreadable` and `staged_bytes` |
| `done` | — |

`no_forecast` says why `days_to_full` is absent: `short_history`, fewer than three samples since free space last jumped up, or samples that span under three hours; or `not_falling`, enough history with free space steady or rising. It is absent with a forecast, and absent with `history_unreadable`, which is its own reason.

`staged_bytes` is the bytes under the state directory's `staging/`, where a run puts what Finder could not take and the docker volume exports. Emptying the Trash never frees them. It is a lower bound where part of the directory cannot be read, and absent when there is nothing there.

`history_unreadable: true` says the headroom history could not be read: the sample was still taken and recorded, but this tick has no forecast and no alerts, and autotrim judged headroom by the top free-space band alone. It is a fact about the tick, not a failure: such a tick still ends with `done` unless something else failed. The field is absent otherwise.

A tick that sampled sends its `tick` event even when it then fails, so the alerts arrive. Errors: `autotrim_locked` (the gate refused a prune that low headroom called for), `tick_failed` (no sample, a history that could not be written, or a prune that failed).

### history

```json
{"type":"history","id":"h1"}
```

What regrow has done on this Mac, read from the oplog and the headroom history, answered beside whatever is in flight; the engine reads them off its request loop, so a `cancel` sent meanwhile is not held up.

| Event | Fields |
|---|---|
| `history` | `runs`: every run in the oplog, oldest first; `samples`: the headroom history's free-space samples, oldest first, each with the `headroom` event's fields (`at`, `total`, `free`, `purgeable`, `swap_used`); `samples_unreadable: true` when that history could not be read, `samples` then empty; `titles`: rule id → title, for each rule the runs counted bytes for that the catalog still holds |
| `done` | — |

A run counts as `regrow history` counts it: an action once its done line is in the journal, a prune or a measured command (the aerial videos' delete) at what it deleted, any other action at its planned size; a failed or cut-short action, and a move `regrow undo` put back, counts nothing. Its fields: `id`; `start`; `done`, `failed` and `undone`, counts of actions; `restorable`, the moves `regrow undo` can still put back; `prune`, true when every action was a prune (autotrim's or `regrow prune`'s); its bytes split by where the space went, as the run's summary splits them: `frees_now` (commands and prunes), `after_trash` (moved to the Trash), `staged` (moved to regrow staging, Finder being unavailable) and `exported` (a docker volume removed after its export); and `rules`, rule id → bytes. A journal that cannot be read fails the request with `history_failed`, since the journal is never read past a bad line.

### aerials and aerials_switch

```json
{"type":"aerials","id":"a1"}
{"type":"aerials_switch","id":"a2","to":"off"}
```

macOS's aerial video downloads, as `regrow aerials` reads and switches them. `aerials` reads the picks idleassetsd downloads for, without root. `aerials_switch` with `to: "off"` records them, journals the root step and clears them, then stops the downloader; with `to: "on"` it puts back what the last off cleared. Its root step goes through macOS's administrator prompt, a system dialog that asks for the owner's password. A switch that has nothing to do (`off` with nothing picked, `on` with nothing recorded) asks for no password, journals nothing and ends with `done`.

| Event | Fields |
|---|---|
| `aerials` | `status`: `{state, version, picks, counts: {categories, subcategories, videos}, detail, recorded: {cleared, picks, restored}, record_error, set_for, set_for_error}`; a switch adds `result`: `{ran, run, before, after, missing}` |
| `done` | — |

`state` is `on` (something is picked), `off`, `absent` (no aerial database on this Mac), `unknown` (it could not be read; `detail` says why) or `unmeasured` (macOS 26 or later, where the picks were not measured, or a macOS version the engine could not read; `detail` says which). `recorded` is the last off's record, and `on` puts its picks back unless `restored` is set. `set_for` names the surfaces the wallpaper store still sets an aerial for, `wallpaper` or `screen saver`. In a switch's `result`, `ran` says whether the root step may have changed the picks (false for one that never started, as at the prompt's Cancel, or that said it changed nothing), `run` is its journal run id, and `missing` counts the recorded picks `on` could not put back because macOS no longer has their category, subcategory or video. The switch's `aerials` event carries the status as it stands after the switch, also when the switch then fails.

Errors: `aerials_failed`, with `message` saying why: the state is not `on` or `off`, the password prompt was canceled (nothing changed), the switch was stopped before its root step started (nothing changed) or before it ended, the root step changed nothing or failed (its exit status, which `osascript` gives only in its error text, decides the record as in a terminal), the record of the picks could not be read or written, the record names no pick regrow can put back, the picks could not be read after `on` put them back, another `regrow aerials off` or `on` is running (nothing changed), the root step could not take the root lock within 30 s, which an earlier one may still hold (the root step changed nothing), the journal could not be written, or this engine cannot read or change the picks.

### rules and set_rules

```json
{"type":"rules","id":"r1"}
{"type":"set_rules","id":"r2","rule":"npm-cache","off":true}
{"type":"set_rules","id":"r3","beta":true}
```

The engine runs the whole catalog as `~/.config/regrow/config.yaml`'s `rules:` says, read again as each scan or tick starts: `off`, the rules it does not look at, and `beta`, whether the rules still in staged rollout run. `--beta-rules` runs them whatever the file says. `set_rules` turns one rule off or on, or the beta rules, by rewriting that key alone and keeping the rest of the file; a change applies from the next scan, and the scan held keeps its findings, but a `plan` or `execute` against it is refused with `rules_changed` while it holds findings of a rule that no longer runs. While the file does not load, from the start or since an edit, no rule runs: a scan, and a `plan` or `execute` against the scan held, fail with `rules_failed`, and a tick with `autotrim` samples and then fails with `tick_failed`, having pruned nothing.

| Event | Fields |
|---|---|
| `rules` | `catalog`: every rule of the catalog, beta ones included, each as `regrow rules --json` prints it plus `off` (the file turns it off) and `runs` (not off, and not beta while the beta rules are off); `beta`, the file's switch; `beta_flag`, true when the engine was started with `--beta-rules`; `config`, the file's path; `error`, present when the file does not load, no rule then running, `off` and `beta` as it last loaded, neither set if it has not loaded since the engine started, and `unknown_off` absent; `unknown_off`, the ids `off` names that no rule has, absent when none |
| `done` | — |

`set_rules` carries `rule` with `off`, or `beta`, never both: anything else is `bad_request`. A rule the catalog lacks, or a file that does not load, fails it with `rules_failed`, and the file is left as it was.

### updates

```json
{"type":"updates","id":"u1"}
```

The `regrow` the install line put in `~/.local/bin`, the one that updates itself, as the engine finds it: answered beside whatever is in flight and off the request loop, since the engine runs that binary's `version`, with `REGROW_NO_AUTO_UPDATE` set so the run checks for nothing, stops it after 5 s and waits 1 s more for its output to close. The app's own update is not the engine's to know.

| Event | Fields |
|---|---|
| `updates` | `cli`: `path`, the binary, the folder's links resolved, absent when there is none; `version`, what its `version` says, absent when it said none; `why`, why it does not update itself (a development build, 0.1.0, which came before the self-update, not a plain file, another user's, a folder it cannot write, a binary that does not run or is not regrow), absent when it does or there is none; `off`, true when `REGROW_NO_AUTO_UPDATE` is set in the engine's environment, which the app takes from the login shell once, as it starts; `checked_at`, when its last daily check began, absent when none is recorded |
| `done` | — |

An engine that cannot name its own path, the home folder or its state folder fails it with `updates_failed`. An engine older than this request answers `unknown_request`.

## hello

```json
{"event":"hello","protocol":1,"version":"0.0.0-dev","fda":"denied","self":"/Users/dev/Applications/Regrow.app/Contents/MacOS/regrow-engine"}
```

`self` is how the owner starts this binary in Terminal, as the sudo skip names it: `regrow` when that name on PATH is this very file, else the executable's path, as inside an app bundle. It is one word as `exec` takes it, not quoted for a shell: a face that shows a command to type, such as `undo`, quotes `self` and starts the command with it. It is left out when the engine was given none.

`fda` says whether this process has Full Disk Access: `granted`, `denied`, or `unknown`. The engine reads one entry of `~/.Trash`, or of `~/Library/Safari` when there is no Trash, and gives up after 2 s (`unknown`). It never probes another app's container, where an open can block forever. A grant takes effect when the app relaunches.

## Errors

```json
{"event":"error","re":"p1","code":"unknown_scan","message":"unknown scan \"…\""}
```

| Code | |
|---|---|
| `bad_request` | not a JSON object of the documented shape, no `id`, or a required field missing; `re` is absent when the line is not JSON or has no string `id` |
| `line_too_long` | the request line was over 1 MiB; no `re` |
| `unknown_request` | unknown `type` |
| `busy` | a scan, execute, tick or aerials switch is in flight |
| `unknown_scan`, `scan_running`, `scan_canceled`, `scan_superseded`, `scan_spent`, `unmatched` | see plan |
| `unknown_plan`, `plan_expired`, `execute_failed` | see execute |
| `autotrim_locked`, `tick_failed` | see tick |
| `history_failed` | see history |
| `aerials_failed` | see aerials |
| `rules_failed` | see rules and set_rules; also a scan, plan or execute while the rules file does not load |
| `rules_changed` | a plan or execute against a scan holding findings of a rule that no longer runs; see rules and set_rules |
| `updates_failed` | see updates |

`message` is for people; peers branch on `code`. An `unmatched` error also lists the selectors in `unmatched`, and an `execute_failed` that ran something carries its `result`.

## Example

```text
← {"event":"hello","protocol":1,"version":"0.0.0-dev","fda":"granted"}
→ {"type":"scan","id":"s1"}
← {"event":"headroom","re":"s1","at":"2026-10-04T10:00:00Z","total":494384795648,"free":53260738560,"purgeable":580311360,"swap_used":4998168576}
← {"event":"start","re":"s1","scan_id":"9f2c01aa-1","rules":2}
← {"event":"finding","re":"s1","scan_id":"9f2c01aa-1","index":1,"took_ms":4,"bucket":"after_trash","select":["xcode-derived-data/~/Library/Developer/Xcode/DerivedData/App-abc"],"finding":{…}}
← {"event":"finding","re":"s1","scan_id":"9f2c01aa-1","index":0,"took_ms":9,"bucket":"frees_now","select":["go-build-cache"],"finding":{…}}
← {"event":"summary","re":"s1","scan_id":"9f2c01aa-1","totals":{…},"exclusive":{…}}
← {"event":"done","re":"s1","elapsed_ms":9,"canceled":false}
→ {"type":"plan","id":"p1","scan_id":"9f2c01aa-1"}
← {"event":"plan","re":"p1","plan_id":"20261004-120000-5b0e…","expires_at":"2026-10-04T12:10:00.25Z","select":["go-build-cache","xcode-derived-data"],"plan":{"actions":[…]},"totals":{…}}
← {"event":"done","re":"p1"}
→ {"type":"execute","id":"x1","plan_id":"20261004-120000-5b0e…"}
← {"event":"journal","re":"x1","entry":{"run":"20261004-120000-5b0e…","seq":1,"event":"start",…}}
← {"event":"journal","re":"x1","entry":{"run":"20261004-120000-5b0e…","seq":1,"event":"done","receipt":{…},…}}
← {"event":"done","re":"x1","result":{"run_id":"20261004-120000-5b0e…","done":1,…},"canceled":false}
```

## regrow scan --json

`regrow scan --json` prints the events of one scan, `headroom` through `done`, exactly as a `scan` request gets them, without `hello` and without `re`. It exits 0 once `done` is written, and 1 when stdout cannot be written (a full disk, a closed descriptor): a stream without its `done` line is never a success.
