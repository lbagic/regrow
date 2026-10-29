# The menubar app

`Regrow.app` keeps regrow in the menu bar: free space, purgeable space, swap and days-to-full at a glance, a watch tick every 15 minutes and on wake whose alerts arrive as notifications, and a window with a sidebar of three sections: Scan, which scans, plans and runs a clean; Stats, what regrow has done on this Mac; and General, which holds Start with Mac, the three grants and where the app keeps its files. Opening the app from Finder, Spotlight or `open` shows the window, also while it runs; a launch at login shows only the menu bar item. The menu's "Open regrow" shows the window as it was left, and "Scan" shows it and starts a scan; a window with no scan offers Scan in its tray. Closing the window hides it, so the scan, plan or run it shows waits for the next Open. While Regrow is in your Login Items, a launch in the first two minutes after you log in counts as the login's and opens no window; opening the app while it runs always shows it, and so does a relaunch. It is a shell around `regrow engine` ([ENGINE.md](ENGINE.md)): the Go binary sits inside the bundle and does all the work. The window follows [DESIGN.md](../DESIGN.md).

It builds with the Command Line Tools alone. No Xcode is needed.

## Install

The install line in the [README](../README.md#install) installs the latest release's app in `~/Applications` on macOS 14 or later, with no developer tools. Each release carries `Regrow.app.zip`, which the release workflow builds on GitHub's macOS runner for Apple silicon and Intel and signs with "regrow release signing", a self-signed identity; `checksums.txt` lists it. The line checks that checksum, and checks that the app satisfies `identifier "com.github.lbagic.regrow" and certificate leaf = H"<app_signer>"`, the certificate's SHA-1 pinned in `install.sh`; when either fails it installs the CLI alone and says why. Then it quits a running Regrow, as its menu's Quit does, stops a leftover engine as `just app` does, puts the app in place with the new bundle's `--swap` and opens it.

From a clone:

```sh
scripts/signing-cert.sh   # once
just app
```

1. `scripts/signing-cert.sh` records the identity `just app` signs with in `.signing-identity` (not committed): "regrow release signing" when the login keychain holds it, so a build here holds the grants a release install got; otherwise "regrow local signing", a self-signed code-signing identity it makes in the login keychain the first time. The first signature with a new key raises a keychain dialog: choose "Always Allow". Run again, it finds the identity, prints the same hash and creates nothing. `--dry-run` only reads the keychain.
2. `just app` builds the app and the engine (from a clean `app/.build` the first time in a checkout, and again after the checkout's folder moves), signs both by that hash with the hardened runtime, asks a running Regrow to quit, waits for its engine to finish the action it is on, exchanges the bundle in `~/Applications` in one step, and launches it. From `~/Applications` the app registers itself to open at login, once, and asks to send notifications. Then it runs the three smoke checks below against the installed app and ends with their `RESULT` line; `just app --no-check` skips them.

macOS ties its grants to the bundle id and the signature. Signed with the same identity, a rebuilt app keeps Full Disk Access, the right to control Finder and its notifications; an app signed with another identity gets each of them again.

The release identity is made once, on the owner's Mac, by `scripts/signing-cert.sh --release` (it needs `gh` signed in as the owner): it makes the repository's `release` environment, which only a `v*` tag or `main` may run a job in, stores the key in its secrets `REGROW_SIGNING_P12` and `REGROW_SIGNING_PASSWORD`, imports it into the login keychain, which keeps the only copy that can be read back, records it, and writes its SHA-1 to `install.sh`'s `app_signer` line, which is then committed. The release workflow signs with no other key: `scripts/release-app.sh` imports the secrets into a keychain of its own, refuses a certificate that is not the pinned one, runs `scripts/app.sh release`, which builds both chips into one bundle and packs it in `app/.build/dist/Regrow.app.zip`, and deletes the keychain. A pull request's run signs with a throwaway identity instead (`release-app.sh --throwaway`), and `workflow_dispatch` on `main` packs an unpublished snapshot signed with the release key. A new key means every Mac grants everything again, so the script makes none while the keychain holds one; run again then, it records and pins that one. `just app-build` makes an ad-hoc signed bundle under `app/.build/bundle` for a look; it keeps no grant, registers nothing, asks for nothing and posts no notification.

## Grants

| Grant | When macOS asks | Without it |
|---|---|---|
| Notifications | first launch from `~/Applications` | no alert reaches you; the menu still shows the figures |
| Control of Finder | the first confirm step that holds a Trash move | Trash moves land in regrow staging; `regrow undo` restores them, emptying the Trash does not free them |
| Full Disk Access | never; granted in System Settings ▸ Privacy & Security, then the app is relaunched; the menu's Full Disk Access row and General's "Open settings" open that pane while it is off | folders macOS protects read as "unreadable" or as a lower bound |

Full Disk Access goes to the app, not to a terminal: the engine inside the bundle is covered by the app's grant.

## What the app runs

- One engine at a time, started again when it exits, with the environment of a login shell, so `go`, `docker` and `brew` are found as they are in a terminal. When that environment cannot be read the menu and the page say so, and a plan skips each command whose program is not on the PATH.
- A watch tick at launch, every 15 minutes and on wake. The autotrim switch sends the tick with `autotrim`; the engine refuses it until one `regrow prune go-build --yes` has completed.
- A scan only when you ask for one: the menu's Scan, the window's Scan or Scan again, or the relaunch that applies Full Disk Access; and again when the engine starts again under a window that shows a scan, hidden or not, since every id of that scan died with the engine. A window whose page stopped and was loaded again shows no scan and says that what it showed is gone; if a run was going when the page stopped, the page says so and names `regrow history` for what it did. Nothing runs without the plan screen and the confirm screen, whose tick box unlocks the button.

**Stats.** What the oplog says regrow has done, as `regrow history` counts it, and the free space the watch has sampled, the 30 days or so it keeps, both read by the engine's `history` request ([ENGINE.md](ENGINE.md)): what runs freed, moved to the Trash and kept as volume tarballs, a chart of free space with a dot for each run that cleaned something, the rules that freed the most, and each run with the `regrow undo` that puts its moves back while it can.

**General.** The window's General section shows the three grants as macOS has them, read again each time the window comes to the front, since System Settings can change any of them: Full Disk Access as the engine's `hello` found it, Control of Finder read without asking (macOS shows no dialog for that), and Notifications. Each that is off opens its pane in System Settings; Full Disk Access then offers "Relaunch to apply". A grant macOS did not report is shown as unknown and not counted as off. It also has the Start with Mac switch, the version, the engine's path, the logs to show in Finder, and regrow staging with its size and the same once something is there. Beside Scan the sidebar says when a scan or a run is going, or the engine has stopped. When a run ends, the shell ticks first, before any scan or tick waiting and without autotrim, so the menu's figures and the staging size are the run's, whether or not a page saw it end.

**Start with Mac.** The menu's "Start with Mac", and the switch in General, show whether Regrow is in your Login Items ("on" or "off") and flip it when chosen. Turning it off there, or removing Regrow in System Settings › General › Login Items, stays off: the app registers itself at launch only until it has done so once. When macOS holds the registration for your approval they say "needs approval", and choosing the row, or General's link, opens Login Items settings. In a build that does not run from `~/Applications` the menu has no such row and General no switch.

The menu's "Show staging in Finder" opens regrow's staging folder: what a run moved there because Finder was not available, and the export made before a docker volume is removed. The app deletes nothing there.

## Checks

```sh
just app-test          # the Swift package's tests
just page-check        # the page through WebKit: assertions and a PNG per state
just app-smoke trash   # through the installed app: a folder it creates goes to the Trash and comes back
just app-smoke alert   # through the installed app: one tick that must raise a notification
just app-smoke doors   # through the installed app: it runs and moves nothing its launch names
just app-smoke all     # the three in turn, then one RESULT line naming any that failed
```

The smoke checks start the installed app through LaunchServices, so the grants they exercise are the app's, and use a state directory of their own. Each prints one `PASS` or `FAIL` line per stage, or one line saying why it did not start, and `all` ends with one `RESULT` line naming any that failed. `all` runs every check even after one fails, but for `doors` beside a `~/regrow-smoke-*` that `trash` left: it fails `trash`, skips `doors` and says so; one that fails leaves its directory in place and names it, with the app's `result.json`, `smoke.log` and `engine.log` inside. `doors` starts the app with `open --env` and `open --args` the ways any program could, each with a probe that leaves a file if it runs: `REGROW_ENGINE`, `HOME` and `ZDOTDIR` for the login shell, `DYLD_INSERT_LIBRARIES`, a smoke folder of another shape and a `--swap` of two other paths. A second argument checks another bundle: `scripts/app.sh signed` builds and signs one as `just app` does, in `app/.build/bundle`, without installing it.

## What a launch can choose

Anything running as you can start the app with variables and arguments of its choosing, and what the app runs then has its Full Disk Access. So a bundle signed with the identity takes nothing from its launch beyond `--open-scan`, which opens the window and starts a scan, and `-autotrim YES`, which turns autotrim on for that run:

- No `REGROW_` variable. `REGROW_ENGINE`, `REGROW_PAGE`, `REGROW_RULES_DIR`, `REGROW_HOME`, `REGROW_STATE_HOME` and `REGROW_LOG` point a development run at other files only in an ad hoc or unsigned build (`just app-build`, `swift run`). The line is the signature, not the path: a copy of the installed app anywhere holds its grants.
- Nothing the engine, the login shell, osascript or the relaunch helper inherit from the app's environment: each starts from the login shell's environment, or from your account's (home, user and shell from the password database, the system `PATH`).
- No `DYLD_` variable: app and engine run with the hardened runtime.
- A smoke check only in a folder `mktemp` made as `$(getconf DARWIN_USER_TEMP_DIR)regrow-smoke.<x>`, and the trash check only on `~/regrow-smoke-*`, through the rule the bundle carries. `just app-smoke trash` refuses to start while one is there already.
- `--swap` only of `~/Applications/.Regrow.app.staged` and `~/Applications/Regrow.app`, from a bundle that resolves where the staged path does, as `just app` runs it; the two are renamed as written, so a link at either is what moves, and a link at the staged path lets any bundle swap them.

What else stays open needs your files changed: the bundle in `~/Applications` is yours to write, a copy of it with its engine replaced still gets Full Disk Access (macOS checks the app's own executable, not the engine inside it), the signing key signs for any program without asking, and the login shell runs your profile with the app's access. So Regrow's Full Disk Access is only as safe as your own files. [ARCHITECTURE.md](ARCHITECTURE.md)'s decisions log has the list.

The app's log is `~/Library/Logs/regrow/app.log`, with the engine's stderr in `engine.log` beside it.

## Known limits

- Neither log is rotated.
- An alert is a crossing, posted once as a notification. The menu shows the figures, not past alerts.
- The first tick on an empty history counts a line free space is already under as crossed, so a state directory with no history and under 50 GiB free posts "Disk space is low" once.
- With autotrim on and its gate locked, the menu says why under the switch. Once free space is low enough that a prune would have run, the tick ends in an error instead: the menu reads "Autotrim did not run", and the notification comes once, not with every tick. Either line quotes the prune to run once, and a menu's text cannot be selected, so under it "Copy the command to run in Terminal" copies it, `regrow` named by the engine's path; the notification says the menu copies it.
- A second copy of the app started on the same state directory says so and quits. Where `app.lock` cannot be locked at all, both copies run, and neither stops an engine an earlier run left.
- Quitting while a plan runs waits up to 30 s for the action in flight, then stops the engine. A steward command the engine had started runs on, or stops part-way once it writes to the engine that is gone; either way the oplog lacks that action's end.
- If the engine stops during a command that exports first, such as a docker volume's removal, the export may sit in the run's folder in regrow staging while the oplog has only the action's start, not where the export went; the window's notice after the stop does not mention it.
- `just app` finds the running app through the pid file in the state directory its own terminal names. It does not stop first an app whose login shell set another `XDG_STATE_HOME`, a copy running from another path, or an app that has not started its engine yet: the first seconds after launch, or up to 37 s while it waits out an engine an earlier run left.
