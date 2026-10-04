# Engine protocol

`regrow engine` lets a shell app (the menubar app) scan, plan and execute without a terminal. It reads requests on stdin and writes events on stdout, one JSON object per line. The code lives in `internal/protocol`; this document is the contract.

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
3. Only one **scan or execute** is in flight at a time. Another `scan` or `execute` sent meanwhile is refused with `error{code:"busy"}` and changes nothing. `plan` and `cancel` are answered at any time.
4. When stdin ends, or the process gets SIGINT or SIGTERM, the engine shuts down: a scan in flight is canceled, an execute finishes the action it is running, journals it and stops there. The terminal events of both are still written. Then the engine exits 0. It waits for that action however long it takes, so a peer that cannot wait out a hung steward command sends SIGTERM twice: the first signal asks for the clean stop, the second kills the engine at once.

Request ids are the peer's own; the engine echoes them and does not check that they are unique.

## Requests

| Request | Fields | Answer |
|---|---|---|
| `scan` | `id` | `headroom`, `start`, one `finding` per rule, `summary`, then `done` |
| `plan` | `id`, `scan_id`, `select` (optional) | `plan`, then `done`; or `error` |
| `execute` | `id`, `plan_id` | one `journal` per oplog line, then `done`; or `error` |
| `cancel` | `id`, `target` | `done`, once `target` has ended |

`tick` arrives with the headroom work (attack plan PR 4).

### scan

```json
{"type":"scan","id":"s1"}
```

Starting a scan makes it the engine's current scan and drops the previous scan's findings and every plan built from them, executed or not.

| Event | Fields |
|---|---|
| `headroom` | the free-space sample's own fields on the event line (`at`, `total`, `free`, `purgeable`, `swap_used`) |
| `start` | `scan_id`: names this scan in `plan`; `rules`: how many `finding` events follow |
| `finding` | `scan_id`; `index`: the rule's position in the catalog; `took_ms`: the rule's own scan time; `finding`: the Finding (below) |
| `summary` | `scan_id`; `totals`: the Totals of every item; `exclusive`: item id → bytes the item frees on its own, nested items excluded |
| `done` | `elapsed_ms`; `canceled`: `true` when the scan was canceled |

`finding` events arrive in completion order, not catalog order. A canceled scan sends no further `finding` and no `summary`, and cannot be planned.

A Finding is `{"rule": Rule, "items": [Item], "error": "…"}`; `error` is set only for a rule-level failure (a tool missing, an unknown query). An item with `partial` and no `path` stands for targets the scan could not enumerate, a folder behind a glob or a discover root, and its label says which; no plan acts on it. Rule is the catalog rule as `regrow rules --json` prints it. An Item is:

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
{"frees_now":0,"after_trash":0,"shown_only":0,"macos_managed":0,"partial":0}
```

`frees_now` is freed as steward commands run; `after_trash` only once the Trash is emptied; `shown_only` is surface-only and never deleted; `macos_managed` is phantom space macOS reclaims itself. `partial` counts items whose bytes are lower bounds.

### plan

```json
{"type":"plan","id":"p1","scan_id":"…","select":["go-build-cache","sim-devices/AAA-111"]}
```

- `select` absent or `null`: the default selection, every safe rule that found something without an error, leaving out unreadable items. A rule with some unreadable items is selected item by item when it acts per item, and left out when its steward command acts on the whole rule.
- `select: []`: an empty plan.
- Otherwise rule ids and item ids, as `regrow clean` takes them.

| Event | Fields |
|---|---|
| `plan` | `plan_id`; `plan`: `{actions, skipped, unmatched}`; `totals` (`frees_now` and `after_trash` are filled) |
| `done` | — |

An action is `{rule_id, item_key, kind, command, pre_action, path, bytes, empties_trash, sudo}`: `kind` is `native` (a steward command, no undo) or `trash` (a Finder move that `regrow undo` restores), `command` is the exact argv, and action bytes never overlap, so they sum to the plan's total. A skip is `{rule_id, item_key, reason}`. `unmatched` lists selectors that addressed nothing in the scan. An action that would need administrator rights is never planned here: it is a skip whose reason names the Terminal command, for example ``needs administrator rights — run `regrow clean spotlight-index` in Terminal``.

A plan id:

- is executable once, for 10 minutes of wall-clock time, sleep included;
- dies when a newer scan starts, even one later canceled;
- dies when any execute starts: that run changes what the scan measured, so every plan of the scan is dropped and planning from it again answers `scan_spent`;
- is the oplog run id its execution journals under, so `regrow history` and `regrow undo <plan_id>` find the run;
- carries 128 random bits, so it cannot be guessed.

Errors: `bad_request` (no `scan_id`), `unknown_scan`, `scan_running`, `scan_canceled` (scan again), `scan_superseded` (a newer scan replaced it), `scan_spent` (an execute ran against it; scan again).

### execute

```json
{"type":"execute","id":"x1","plan_id":"…"}
```

The engine executes on request: the peer's confirmation is the per-run opt-in. Actions run in plan order, one at a time. Every oplog line is synced to disk before its `journal` event is written, and an action's `start` line is synced before the action runs.

| Event | Fields |
|---|---|
| `journal` | `entry`: the oplog line, `{time, run, seq, event, rule_id, item_key, kind, command, path, bytes, receipt, error}`; `event` is `start`, `done` or `fail`; a done Trash move carries `receipt: {original, to, method}` |
| `done` | `result`; `canceled`: `true` when a cancel or shutdown stopped the run before every action was attempted |

`result` is `{run_id, done, failed, bytes, trash_bytes, staged_bytes, failures, skipped, stopped}`. `bytes` counts successful actions; `trash_bytes` is the part now in the Trash, freed once it is emptied; `staged_bytes` the part moved into regrow's staging directory because Finder was unavailable, which emptying the Trash never frees. One failed action does not stop the run.

A steward command runs with stdin from `/dev/null` and its stdout and stderr captured. When it fails, the last 4 KiB of that output ends the `fail` line's `error`. The docker export that precedes a volume removal is captured the same way, except that its stdout is the tarball.

A Trash move that has not finished after 60 s fails with a reason, and the run goes on. A folder macOS blocks without Full Disk Access can hold a move forever, and no cancel reaches it. If Finder completes such a move later, Finder's Put Back restores the item; `regrow undo` has no receipt for it.

Errors: `bad_request` (no `plan_id`); `busy`; `unknown_plan` (never issued, already executed, or dropped when an execute or a newer scan started); `plan_expired`; `execute_failed` (the run could not start, for example an unreadable config file, or the journal failed and nothing more runs). The first execute that names a plan id spends it, whatever the outcome.

### cancel

```json
{"type":"cancel","id":"c1","target":"s1"}
```

`target` is the `id` of a scan or execute. A scan stops at once. An execute finishes the action it is running, journals it, and runs nothing after it. The target then ends with its `done`, and only after that does the cancel get its own `done`, so a `scan` sent after the cancel's `done` is never refused as busy. A target that is not in flight has nothing left to do: the cancel gets `done` at once.

The target's `done` says whether the cancel cut it short. Peers read `canceled`; an execute's `result.stopped` carries the same value. A cancel that lands as the target finishes (during an execute's last action, or as a scan wraps up after its last finding) can come too late to change anything: the target then ends `canceled:false` with its full results, and a scan that ends that way can be planned.

## hello

```json
{"event":"hello","protocol":1,"version":"0.0.0-dev","fda":"denied"}
```

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
| `busy` | a scan or execute is in flight |
| `unknown_scan`, `scan_running`, `scan_canceled`, `scan_superseded`, `scan_spent` | see plan |
| `unknown_plan`, `plan_expired`, `execute_failed` | see execute |

`message` is for people; peers branch on `code`.

## Example

```text
← {"event":"hello","protocol":1,"version":"0.0.0-dev","fda":"granted"}
→ {"type":"scan","id":"s1"}
← {"event":"start","re":"s1","scan_id":"9f2c01aa-1","rules":2}
← {"event":"finding","re":"s1","scan_id":"9f2c01aa-1","index":1,"took_ms":4,"finding":{…}}
← {"event":"finding","re":"s1","scan_id":"9f2c01aa-1","index":0,"took_ms":9,"finding":{…}}
← {"event":"summary","re":"s1","scan_id":"9f2c01aa-1","totals":{…},"exclusive":{…}}
← {"event":"done","re":"s1","elapsed_ms":9,"canceled":false}
→ {"type":"plan","id":"p1","scan_id":"9f2c01aa-1"}
← {"event":"plan","re":"p1","plan_id":"20261004-120000-5b0e…","plan":{"actions":[…]},"totals":{…}}
← {"event":"done","re":"p1"}
→ {"type":"execute","id":"x1","plan_id":"20261004-120000-5b0e…"}
← {"event":"journal","re":"x1","entry":{"run":"20261004-120000-5b0e…","seq":1,"event":"start",…}}
← {"event":"journal","re":"x1","entry":{"run":"20261004-120000-5b0e…","seq":1,"event":"done","receipt":{…},…}}
← {"event":"done","re":"x1","result":{"run_id":"20261004-120000-5b0e…","done":1,…},"canceled":false}
```

## regrow scan --json

`regrow scan --json` prints the events of one scan, `headroom` through `done`, exactly as a `scan` request gets them, without `hello` and without `re`. It exits when the scan is done.

## Not yet sent

`headroom` and the `tick` request arrive with the headroom work (attack plan PR 4). Until then a scan starts with `start`.
