#!/bin/sh
# Checks the installed Regrow.app end to end. The app is started through
# LaunchServices, so macOS attributes what it does to Regrow and not to
# this terminal. Both checks use a state directory of their own: the
# oplog and the headroom history are not touched.
#
#   scripts/app-smoke.sh trash   Moves a folder this script creates in your home
#                                to the Trash through the app and restores it with
#                                undo. The first run raises "Regrow wants to
#                                control Finder": allow it.
#   scripts/app-smoke.sh alert   One watch tick against a made-up history in which
#                                free space just fell 6 GiB: one notification
#                                should appear.
#   scripts/app-smoke.sh doors   Starts the app the ways anything on this machine
#                                can (open --env, open --args) and shows it runs and
#                                moves nothing they name. No notification, no Trash.
#
# A second argument names another bundle to check, such as the one
# `scripts/app.sh signed` leaves in app/.build/bundle.
#
# A check that fails leaves its directory in place and names it: the
# app's result.json, smoke.log and engine.log are in it.
set -eu

APP="${2:-$HOME/Applications/Regrow.app}"
mode="${1:-}"
case "$mode" in
trash | alert | doors) ;;
*)
	echo "usage: $0 trash|alert|doors [bundle]" >&2
	exit 64
	;;
esac
if [ ! -d "$APP" ]; then
	echo "$APP is not installed: run \`just app\` first." >&2
	exit 1
fi

# The app takes no other folder: the per-user temp folder from getconf, not
# TMPDIR, and a name of this shape.
work=$(mktemp -d "$(getconf DARWIN_USER_TEMP_DIR)regrow-smoke.XXXXXX")
mkdir -p "$work/state/regrow"

finish() {
	if [ "$1" -eq 0 ]; then
		rm -rf "${work:?}"
	else
		echo "Left for a look: $work" >&2
	fi
	exit "$1"
}

# The app writes result.txt: one "PASS|FAIL <stage>: <detail>" line per
# stage, then "RESULT ok" or "RESULT failed".
report() {
	if [ ! -f "$work/result.txt" ]; then
		echo "FAIL: the app wrote no result. Its log: ~/Library/Logs/regrow/app.log" >&2
		return 1
	fi
	cat "$work/result.txt"
	grep -q '^RESULT ok$' "$work/result.txt"
}

# One sample ten minutes old, $1 bytes above free space now. Swap as it is
# now, so the fall in free space is the only change.
history() {
	total=$(df -k /System/Volumes/Data | awk 'NR==2 { printf "%.0f", $2 * 1024 }')
	free=$(df -k /System/Volumes/Data | awk 'NR==2 { printf "%.0f", $4 * 1024 }')
	before=$(awk -v f="$free" -v d="$1" 'BEGIN { printf "%.0f", f + d }')
	swap=$(sysctl -n vm.swapusage | awk '{ printf "%.0f", $6 * 1024 * 1024 }')
	printf '{"at":"%s","total":%s,"free":%s,"purgeable":0,"swap_used":%s}\n' \
		"$(date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)" "$total" "$before" "$swap" >"$work/state/regrow/headroom.jsonl"
}

if [ "$mode" = doors ]; then
	probe="$work/probe"
	marks="$work/marks"
	mkdir -p "$probe/home" "$probe/zdotdir" "$probe/elsewhere" "$probe/swap-a" "$probe/swap-b" "$marks"
	# Each probe leaves a file named for its door in $marks when it runs.
	printf '#!/bin/sh\n: >"%s/REGROW_ENGINE"\n' "$marks" >"$probe/engine"
	chmod +x "$probe/engine"
	for file in .zshenv .zprofile .zshrc .bash_profile .profile; do
		printf ': >"%s/HOME"\n' "$marks" >"$probe/home/$file"
		printf ': >"%s/ZDOTDIR"\n' "$marks" >"$probe/zdotdir/$file"
	done
	printf '#include <stdio.h>\n__attribute__((constructor)) static void mark(void) { FILE *f = fopen("%s/DYLD_INSERT_LIBRARIES", "w"); if (f) fclose(f); }\n' \
		"$marks" >"$probe/mark.c"
	clang -dynamiclib -o "$probe/mark.dylib" "$probe/mark.c"
	echo a >"$probe/swap-a/which"
	echo b >"$probe/swap-b/which"
	history 0

	# `open -W` cannot wait for an app that has already exited. zsh reads
	# ZDOTDIR before HOME, so each has a start of its own.
	open -n -W --env REGROW_ENGINE="$probe/engine" --env HOME="$probe/home" \
		--env DYLD_INSERT_LIBRARIES="$probe/mark.dylib" "$APP" --args --smoke-alert "$work" 2>/dev/null || true
	open -n -W --env ZDOTDIR="$probe/zdotdir" "$APP" --args --smoke-alert "$work" 2>/dev/null || true
	open -n -W "$APP" --args --smoke "$probe/elsewhere" 2>/dev/null || true
	open -n -W "$APP" --args --swap "$probe/swap-a" "$probe/swap-b" 2>/dev/null || true

	status=0
	if [ ! -f "$work/result.txt" ]; then
		echo "FAIL started: the app wrote no result, so no variable was tried. Its log: ~/Library/Logs/regrow/app.log"
		status=1
	fi
	for door in REGROW_ENGINE HOME ZDOTDIR DYLD_INSERT_LIBRARIES; do
		if [ -e "$marks/$door" ]; then
			echo "FAIL $door: the app ran what it named"
			status=1
		else
			echo "PASS $door: ignored"
		fi
	done
	if [ -e "$probe/elsewhere/result.txt" ]; then
		echo "FAIL --smoke: the app ran a smoke test in $probe/elsewhere"
		status=1
	else
		echo "PASS --smoke: refused a folder of another shape"
	fi
	if [ "$(cat "$probe/swap-a/which")" = a ] && [ "$(cat "$probe/swap-b/which")" = b ]; then
		echo "PASS --swap: refused two other paths"
	else
		echo "FAIL --swap: the app swapped $probe/swap-a and $probe/swap-b"
		status=1
	fi
	if [ "$status" -eq 0 ]; then echo "RESULT ok"; else echo "RESULT failed"; fi
	finish "$status"
fi

if [ "$mode" = alert ]; then
	history $((6 * 1024 * 1024 * 1024))
	open -n -W "$APP" --args --smoke-alert "$work"
	status=0
	report || status=1
	finish "$status"
fi

token="regrow smoke $$ $(date +%s)"
sentinel="$HOME/regrow-smoke-$$"
mkdir "$sentinel"
printf '%s\n' "$token" >"$sentinel/sentinel.txt"

# The app's own rule lists ~/regrow-smoke-*, and nothing else.
open -n -W "$APP" --args --smoke "$work"

status=0
report || status=1

if [ "$(cat "$sentinel/sentinel.txt" 2>/dev/null)" = "$token" ]; then
	echo "PASS restored: $sentinel is back with its content."
	rm -rf "${sentinel:?}"
else
	status=1
	if [ -e "$sentinel" ]; then
		echo "FAIL restored: $sentinel is there but its content changed. It is left as it is." >&2
	else
		echo "FAIL restored: $sentinel was not put back. It is in the Trash as regrow-smoke-$$, or under $work/state/regrow/staging." >&2
	fi
fi
finish "$status"
