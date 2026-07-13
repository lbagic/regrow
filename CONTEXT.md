# regrow

A disk cleaner that knows what everything is, what regenerates, and how. One context: the whole product speaks this language.

## Language

### Catalog

**Rule**:
One declarative cleaning target: what it is, where it lives, how risky, how it regenerates. Data (YAML), never code.
_Avoid_: cleaner, module, task

**Catalog**:
The full set of rules compiled into the binary (or loaded from a rules directory).

**Tool query**:
A named code hook a rule references when only a steward tool can enumerate its targets (docker df, simctl list).
_Avoid_: plugin, integration

**Fixture**:
A rule's own test data: the files planted in a fake home and the fake tool-query results its golden test runs against. Travels inside the rule.
_Avoid_: test data, mock setup

### Scanning

**Host**:
The machine rules are resolved against: OS, version, home, and a re-anchorable root. Fake hosts make every rule testable.

**Finding**:
One rule plus everything the scan measured for it.
_Avoid_: result, entry

**Item**:
One concrete thing a rule found: a directory, or a tool-reported entry like a docker image.

**Regen story**:
What brings deleted data back and what that costs. Shown next to every finding; the product's core promise.

### Planning

**Plan**:
The dry-run output: the exact commands that would run, plus what was deliberately skipped and why.

**Action**:
One exact command the plan would run — a steward command or a move to the Trash.

**Steward command**:
The tool's own cleanup command, preferred over raw deletion (`go clean -cache`, `simctl runtime delete`).
_Avoid_: native command (in prose; the YAML field keeps its name)

**Preview command**:
The exact command the trash mechanism would run for a path, shown on the plan screen before anything executes.

**Risk class**:
Architectural handling class of a rule: safe (auto-clean), caution (review), surface-only (never deletable through regrow).
_Avoid_: severity, danger level

**Surface-only**:
Shown so you know it exists; regrow never deletes it (iOS backups, Docker VM disk).

### Safety

**The fence**:
The non-negotiable safety invariants: dry-run default, trash-not-rm, path guards, oplog before action, surface-only never deletable.

**Path guard**:
The check every destructive target must pass: never empty, root, home, top-level, or mount roots.

**Oplog**:
The journal every executed action lands in before it runs; the source for undo and history.
_Avoid_: audit log

**Golden test**:
A rule's snapshot test: scan its fixture, plan, compare the normalized command list. Every rule has one.
