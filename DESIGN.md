# Design

The look of regrow's window (`app/Resources/page.html`): a sidebar of its sections; the scan, with the findings, the plan, the confirm step, the run and its result; Stats, what regrow has done on this Mac; General, the app's start, grants and whereabouts; and Rules, every rule and which of them run. The scan is a reading list: the consequence as a headline, the safe findings folded into one line, and each decision that is the owner's as a short text with its size drawn beside it.

## Tokens

```css
:root {
  --paper: #fafbf8; --panel: #eff2ec; --ink: #18211b; --ink-2: #56635a; --ink-3: #88948b;
  --line: #d9dfd6; --line-strong: #c3ccc0;
  --accent: #1e6a45; --accent-soft: #ddebdf; --on-accent: #ffffff;
  --caution: #9a5b00; --danger: #b3261e; --on-danger: #ffffff;
  --taken: #7b887f; --purge: #b3beb5; --track: #e2e7df;
  --b-now: #3280de; --b-trash: #039868; --b-export: #c2780f; --b-shown: #4a3ba8; --b-macos: #da6e97;
  --serif: ui-serif, "New York", Georgia, serif;
  --sans: system-ui, -apple-system, BlinkMacSystemFont, sans-serif;
  --mono: ui-monospace, "SF Mono", Menlo, monospace;
}
/* dark */
  --paper: #131714; --panel: #1b211c; --ink: #e6ece5; --ink-2: #9daa9f; --ink-3: #6e7a71;
  --line: #2b332d; --line-strong: #3c463e;
  --accent: #78c79a; --accent-soft: #1f3327; --on-accent: #0e1a12;
  --caution: #e0a84a; --danger: #f08a80; --on-danger: #2a0f0c;
  --taken: #8d9a90; --purge: #56625a; --track: #2a322c;
  --b-now: #4795f4; --b-trash: #30ac7d; --b-export: #d98f2e; --b-shown: #6456b5; --b-macos: #b53266;
```

The page follows `prefers-color-scheme`; `data-theme="light|dark"` on `<html>` overrides it for a screenshot.

## Colour

- `--accent` is the one action colour: links, the primary button, the progress bar, the current step, a tick box, a switch that is on.
- `--danger` marks what cannot be undone or did not work: the run button and its tick box, the "Not scanned" heading, error text. `--caution` colours an icon only, never a fill.
- The five `--b-*` colours each stand for one total, on every mark of that total and on nothing else: its swatch, its rule, its size bars, its meter segment. `--b-now` frees now, `--b-trash` after the Trash is emptied, `--b-export` kept as tarballs until they are deleted (a docker volume's export; it appears only when a scan or plan holds one), `--b-shown` shown only, `--b-macos` macOS-managed. They hold apart for every pair, also under protanopia and deuteranopia, only at these values: a change to one is checked against the other four in both themes. `--b-export` was added by that check: simulated with the Machado matrices, its CIE76 distance to each other colour is at least 36 in the light theme and 39 in the dark one under protanopia and deuteranopia, where the closest pair of the other four is 13.8 (`--b-trash` and `--b-macos`, deuteranopia, light).
- `--taken`, `--purge` and `--track` draw the disk meter: taken, purgeable, free. `--track` also fills the area under Stats' free-space line, and `--ink-3` the bars of Stats' By rule and a run's part in regrow staging, which are no total.
- Every surface is flat `--paper`. `--panel` fills only the sidebar, the tray and a notice; `--accent-soft` only the sidebar's current section, a row that arrived a moment ago, a finished step, and the plan's group whose action is running.

## Type

Serif for what is read as a sentence (the headline, section and finding titles, a finding's story, the plan's lead paragraph); sans for labels, figures and controls; mono at 12px for commands, paths and error text.

| | |
|---|---|
| Headline | serif 600 23/1.2; 28 on the confirm step |
| Section title, the name in the top bar | serif 600 17 |
| Finding title / plan action title | serif 600 16 / 15 |
| Story | serif 400 14/1.45, at most 62ch |
| Body | sans 13/1.4 |
| Secondary line | sans 12.5 in `--ink-2`; 12 for the smallest |
| A total / a row's size | sans 650 20 / 15, tabular figures |
| Button | sans 650 13.5 |

A size is the figure and its unit set apart: the unit at regular weight in `--ink-2`, one step smaller.

## Layout

- A 196px sidebar runs the window's height; beside it three rows: a 42px top bar, the scrolling page, a 60px tray, which only the scan has. 32px of side padding. Drawn for 1096×640, the scan 900 wide; the window is never smaller than 916×520.
- Rows are separated by 1px `--line`; a section header, the tray's top edge and the sidebar's right edge by `--line-strong`.
- A finding row is a grid: 28px for the tick box, the text, and a 246px size column (a 148px bar, a 10px gap, an 88px figure). A row with nothing to tick has no tick-box column.
- Radius: 8px on buttons, notices and highlighted rows; 2 to 3px on bars and swatches.
- Icons are inline SVG, 14px, 1.5px stroke in `currentColor`.
- The page behaves as an app with words you can quote: a default cursor, a text cursor over commands, paths and error text, and the page's text selectable for copying, its controls, the top bar and the tray not. A region is drawn again only when what it shows changed, so a selection lasts until it does. Focus is a 2px `--accent` outline, offset 2px.
- **Copy**: every command, path and id the page shows for you to act on has a "Copy" link 8px after it (sans, the `--accent` link style), reading "Copied" once clicked, until another is. It copies the text whole: a path drawn with `~` is copied from its home, a command as a shell reads it. A rule's or the engine's sentence quotes its commands and paths in backticks, which the page sets in mono; a quote holding a space or a slash gets a Copy, a bare name does not. A command the sentence brings in with a colon is one to run yourself: it is set on a line of its own, in `--ink`, its Copy beside it, and the stop after it is not drawn. A command with a `{path}` or `{arg}` placeholder, as a finding's row shows it, gets none: the plan's lines carry each command whole. It is drawn as a shell reads it all the same, the placeholder as written.

## The sidebar

The name, serif 600 17 in the top bar's 42px; one 30px line per section: a 14px icon, the name in sans 13, and on the right, sans 12 `--ink-2`, what needs you there: "scanning" or "running" beside Scan while it is busy, "stopped" while the engine is down, what runs have freed beside Stats, how many rules run beside Rules, the caution icon and how many grants are off beside General. "Settings", sans 600 11 in capitals and `--ink-3`, heads the sections that set the app up. The current section is filled `--accent-soft`, its name `--ink` 650, its icon `--accent`. At the foot: free space (sans 650 15) of the total, the disk meter, and the forecast in words. Another section and back leaves the scan as it was, scrolled where it was.

## The page, top to bottom

1. **Top bar**: the section's name ("Scan"), the scan's status in words, one action on the right ("Scan again" once there is a scan; while scanning, a progress bar and "Stop"). Empty on the plan and confirm steps.
2. **Headline**: with a forecast, when the disk fills at the recent pace; without one, free space of the total, and a line under the meter saying why there is no forecast.
3. **Disk meter**: 6px, three segments with 2px gaps, then the figures in words: free, purgeable, swap.
4. **Four totals**, side by side, and a fifth, the volume exports, only when the scan holds a docker volume: a 4px rule filled against the largest of them, the figure, a swatch and a label saying when the space comes back. They are never added up, and a line under them says so. While a scan runs each reads "pending".
5. **Notice** (Full Disk Access off; macOS downloading aerial videos, an aerial still set, a switch of the downloads that did not end, or an `on` that could not put every pick back): one `--panel` row each, an icon, a sentence, its action on the right. The aerials notice shows where the findings are or would be, never on the plan or confirm step; its action is "Turn off aerial downloads" while anything is picked, "Turn them back on" while the downloads are off and the last off has picks to put back, none while they are off with nothing to put back, or, in `--ink-2` and unbuttoned, "Waiting for your password…" while macOS asks and "Waiting for the engine…" while the switch waits for a scan, run or check to end, or for the engine to start. Each warning for an aerial still set is a line of its own, with "Open Wallpaper settings" or "Open Screen Saver settings" as a link 8px after it, which opens that pane of System Settings. Why a switch did not end is a line in mono, as an engine error is; what an `on` could not put back is a line of its own in the notice's text when it ended, and follows why, in that mono line, when it did not.
6. **Sections**, in this order, each only when it has rows: Not scanned, Safe to clear, Your call, Only shown. A header is the title, a count in words, and on the right the bar scale or a link.
7. **Tray**: the three steps (Findings, Plan, Confirm) as numbered circles, a sentence on where things stand, the next action as the one filled button. On the findings, the sentence counts what is ticked, the skips its plan would have, and what that frees, by total. With no scan yet, under "No scan to show.", it says a scan removes nothing, and the button is Scan.

## Findings

- **Safe to clear** is folded into one ticked line: the six largest by name and size, "and N smaller ones", then one sentence. "Show each one" opens it into compact sans rows; "Fold into one line" closes it.
- **Your call**: the title with its item count, the regeneration story, then one line with what it costs to bring back and what runs. On the right the bar, the size, when the space comes back, and when it was last used.
- **Only shown** rows carry the rule's note in place of the cost line.
- The aerial videos' row says, under its cost or note line, where the downloads stand ("Aerial downloads are on: 4 categories picked.", "Aerial downloads are off; regrow keeps what it cleared.", "Aerial downloads are off: nothing is picked."), with the switch as a link 8px after it where there is one: off while anything is picked, "Turn them back on" while regrow holds picks it cleared. While they are off, a line under it gives the `regrow clean aerial-wallpapers` to run in Terminal, since the delete asks for a password, and on a row the scan kept, once the downloads read off, a last line says its note is from the last scan and a scan again offers the videos; the window reads the downloads again after each scan.
- A lower bound is written "≥ " before the figure. A row nothing could be read from has no bar and no figure: a lock, "unreadable", and what it needs.
- A row whose folder also holds other rows draws and writes its own bytes, and says under them how much the folder holds.
- A rule that failed is in "Not scanned": its title, the error in mono `--danger`, and no size.
- **A finding's items**: a finding with more than one item gives its count beside the title as an `--accent` link ("10 of 12 folders"; "4 items" where nothing can be picked one by one). It opens into compact rows under the finding's text: a tick box only where the item can be picked, the path in mono with `~` (or the item's label), its last use as a secondary line, and on the right a bar and size on the list's shared scale. An item nothing could be read from shows the lock and "unreadable".
- A tick box exists only where the engine handed the row its atoms; the page has no rule of its own about what may be deleted.

## Size bars

A 6px bar on a 1px `--line-strong` hairline the full width of its column; the fill starts at the left in the total's colour, its right end rounded, at least 2px wide. All bars of a list share one scale, a full bar being the largest row, and the first section header that draws a bar says so. A bar always sits beside its written size.

## Plan and confirm

- **Plan**: a headline with the number of actions; one paragraph, with swatches, on what frees as commands run and has no undo and what goes to the Trash; the disk meter as it will stand, with "free once the commands end". Then "Skipped" with each reason, before "In the order they run": a sequence number (a range for a group), the title, the first command or path in mono with "and N more like it" (a link that opens the group to every line), what runs first and what else is removed, and on the right the bar, the size, and "no undo" or "undo restores".
- **Confirm**: one centred column, at most 560px: the question as the headline, a list pairing each figure with a sentence, then a tick box that unlocks the `--danger` button. Return never runs the plan.
- **The run**: the plan's list stays, every line of each group shown and marked by a 14px icon: a ring waiting, a filled dot running, a tick done in `--accent`, a cross failed in `--danger` with its error in mono `--danger` under it, a dash skipped. The running group takes `--accent-soft`, and its size column says in words what became of its lines ("2 done, running"). The top bar holds the progress and "Stop after this action"; the tray has no button.
- **The result**: a headline counting what was done, failed and skipped; a paragraph with swatches on what is free now, what is in the Trash and what is in regrow staging; `regrow undo <run id>` in selectable mono whenever anything went to the Trash or staging, `regrow` being the command the engine's `hello` names (an app's engine by its path), quoted for a shell; under it, the run's folder in regrow staging, as the run's `done` names it, whenever anything is there. "Failed" comes first, as a `--danger` section, then the list as it ran, marked. "Scan again" is the one way on.

## Stats

From the engine's `history`: the oplog's runs and the free-space samples. Asked for when the engine says hello, when a run ends and when Stats is opened.

- **Headline**: what runs freed as commands ran, since the first run; under it, how many runs, how many had a failure, and what Go build cache prunes freed of it. With no run yet, "Nothing has run on this Mac yet."
- **Totals**, as the scan's, never added up: freed as commands ran, moved to the Trash, kept as docker volume tarballs, each only when there is some (freed always); what went to regrow staging, in words under them.
- **Free space**: one 2px `--ink-2` line over the samples on a scale from zero, its area `--track`, gridlines `--line` with their figures in `--ink-3` 11; a 9px `--b-now` dot with a 2px `--paper` ring where a run that cleaned something ran; dates under it, with times for a span under four days; a crosshair and a tip on hover; a legend for the line and the dots. Too few samples, or a history that cannot be read, is a sentence.
- **By rule**: the eight largest, each a compact row with its title, a bar on the list's scale and its size.
- **Runs**, the latest first: when, then how many actions (a run of prunes is "Go build cache prune"), a `--danger` tag for failures, the `regrow undo` line with Copy while a move can be put back; on the right a bar split into its totals, on the list's scale, and the run's size.

## General

Startup, Permissions, Updates, About. A setting is a row: on the left its name (sans 650 13.5) and a sentence on what it does or what being off costs (12.5 `--ink-2`); on the right its state in words, then its control, a switch, a link, or the small filled button when that is the one thing to do. The switch is 32×19, `--line-strong` off, `--accent` on, a `--paper` knob; it moves when the shell says the setting changed, not on the click.

- **Start with Mac**: a switch, as the menu's row; "needs approval" with a link to Login Items settings; outside `~/Applications`, a sentence and no control.
- **Permissions**, its header counting those off: Full Disk Access (the lock beside its name while off, what that cost the last scan, "Open settings", then "Relaunch to apply"); Control of Finder; Notifications. A grant that is off links to its pane in System Settings, and Notifications on does too.
- **Updates**: Regrow.app, its version and "Check for update", then "Update and relaunch" when a release is newer; `regrow` in Terminal, the install line's, read as General opens: "on" with its version, its path, when it last checked, and the line that turns that off with Copy and that the row reads it again when Regrow next starts; "off" with why; "not installed" when the install line put none there. That row has no button, since it updates itself.
- **About**: the version, the engine's path with Copy, the logs with "Show in Finder", and regrow staging, its size and "Show in Finder" once something is there.

A grant macOS did not report, Full Disk Access or Control of Finder "unknown", is said so and not counted as off.

## Rules

From the engine's `rules`. The headline counts the rules that run of all; under it, what the beta rules are, the config file both the app and `regrow` in Terminal read (its path with Copy), and that a change applies from the next scan. A file that no longer loads is a notice, and so is a change that did not take.

- **Preferences**: autotrim, the menu's switch; Scan beta rules, a switch, or "on, by --beta-rules" with none.
- **The filter**: a search field and All, On, Off, Beta with their counts; it hides rows in place.
- **A section per category**, the catalog's order: each rule a compact setting row, its title (sans 650 13) and tags for its risk ("safe to clear", "your call", "only shown") and "beta"; under it, where it looks and the first sentence of its story, in 12. On the right, what the last scan found for it, "off", or "beta, not scanned", and its switch, greyed while the beta rules are off for a beta rule. A switch moves when the engine says the change held.

## Wording

- Sizes as `engine.HumanBytes` prints them.
- Say when space comes back, in plain words: "frees now", "after the Trash is emptied", "kept as tarballs until you delete them", "shown only", "macOS reclaims it by itself".
- Ages: today, yesterday, N days ago, N months ago.
- The home folder is written `~`, except in a command the owner is told to paste into Terminal and in a tool's own error text, which stay as written. A command or path wraps at a space or after a slash, never inside a name; the command, as a shell reads it, is in the element's `title`.

## The menu

The menu bar item is the drive symbol and the free space. Its menu is native, drawn by AppKit: what the owner reads at the top, what they can do under it. A line that does nothing is a view in a disabled item: the arrow keys pass over it, and nothing greys it, as macOS greys a disabled item's title. None of the page's colours or type reaches it; sizes are the system font's.

- **Figures**, 340pt wide with 14pt sides, in the label colour: free space semibold 20 and "free of" the total in 13; a 6px meter (taken, purgeable, free, 2px gaps, the label colour at 55%, 25% and 10%, radius 3; free to scale, purgeable at least 2px); "Purgeable · Swap" and the forecast in 12; "Checked at" in secondary 11; then a failed check or a stopped engine in 12, wrapped.
- **Actions**, in this order, separated where shown: Open regrow, which shows the window as it was left, and Scan, which shows it and starts a scan | the autotrim switch, its state on the right in secondary ("on", "off"; a checkmark would indent every item out of line with the text above), with the engine's note under it in secondary 12, wrapped, its quotes in mono, when autotrim did not run; "Start with Mac" drawn the same way ("on", "off" or "needs approval"); the aerial downloads' switch the same way ("on", "off", "asking…" while macOS asks for the password, "waiting…" while the switch waits for a scan, run or check to end, or for the engine to start), a line that does nothing when they are off with nothing to put back, and absent where regrow cannot switch them; under it, or in its place where it is absent, why a switch did not end or what an `on` could not put back and, for an aerial still set, a warning and "Open Wallpaper settings" or "Open Screen Saver settings" | Full Disk Access with its state on the right, which opens its pane in System Settings while it is not granted, "Relaunch to apply" under it; "Show staging in Finder" with the staging size on the right in secondary, when there is any | the login environment's failure, wrapped | Quit regrow.
- A menu's text cannot be selected: under any line that quotes a command, "Copy the command to run in Terminal" copies it.

## Checking a change

`just page-check` draws every state through WebKit, the engine the app's web view uses, at 1096×640, from the recorded engine transcript, and writes a PNG of each: results, plan, confirm, Stats, General and Rules in light and dark, every other state in light. Hold each against this file.
