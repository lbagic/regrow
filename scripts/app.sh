#!/bin/sh
# Builds Regrow.app with the Command Line Tools alone (no Xcode).
#
#   scripts/app.sh build     assemble app/.build/bundle/Regrow.app, ad-hoc signed.
#                            It runs, but macOS keeps no grant for it across rebuilds.
#   scripts/app.sh signed    the same, signed with the identity scripts/signing-cert.sh
#                            recorded: the installed app's twin, left in place for checks.
#   scripts/app.sh install   build, sign with that identity, replace
#                            ~/Applications/Regrow.app, launch it, then run
#                            `scripts/app-smoke.sh all` against it. --no-check skips
#                            the checks.
#   scripts/app.sh release   the signed bundle for both chips, packed in
#                            app/.build/dist/Regrow.app.zip as a release carries it.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BUNDLE="$ROOT/app/.build/bundle/Regrow.app"
DEST="$HOME/Applications/Regrow.app"
DIST="$ROOT/app/.build/dist"
ZIP="$DIST/Regrow.app.zip"
RECORD="$ROOT/.signing-identity"
STATE="${XDG_STATE_HOME:-$HOME/.local/state}/regrow"
SIGN_LIMIT=60
QUIT_LIMIT=45

mode="${1:-}"
check=yes
case "$mode $*" in
"install install --no-check") check=no ;;
"build build" | "signed signed" | "install install" | "release release") ;;
*)
	echo "usage: $0 build|signed|install|release [--no-check]" >&2
	exit 64
	;;
esac

identity=-
if [ "$mode" != build ]; then
	if [ ! -s "$RECORD" ]; then
		echo "No signing identity recorded. Run scripts/signing-cert.sh once, then this again." >&2
		exit 1
	fi
	identity=$(tr -d '[:space:]' <"$RECORD")
	case "$identity" in
	*[!0-9A-Fa-f]* | "")
		echo "$RECORD does not hold a SHA-1 hash." >&2
		exit 1
		;;
	esac
	if [ "${#identity}" -ne 40 ]; then
		echo "$RECORD does not hold a SHA-1 hash." >&2
		exit 1
	fi
fi

# codesign waits forever on a keychain dialog nobody answers. The hardened
# runtime makes dyld ignore DYLD_ variables, which `open --env` can set.
sign() {
	codesign --force --timestamp=none --options runtime \
		--entitlements "$ROOT/app/Regrow.entitlements" --sign "$identity" "$@" &
	signer=$!
	(
		sleep "$SIGN_LIMIT"
		kill "$signer" 2>/dev/null
	) >/dev/null 2>&1 &
	watchdog=$!
	status=0
	wait "$signer" || status=$?
	kill "$watchdog" 2>/dev/null || true
	wait "$watchdog" 2>/dev/null || true
	if [ "$status" -ne 0 ]; then
		echo "codesign failed or did not finish within ${SIGN_LIMIT}s." >&2
		echo "If a keychain dialog is open, choose \"Always Allow\" and run this again." >&2
		exit 1
	fi
}

# The executable a pid runs, or nothing when the pid is gone.
exe_of() {
	ps -o comm= -p "$1" 2>/dev/null || true
}

# Signals pid only while it still runs the executable given: a pid that has
# gone may have passed to another program.
signal_if() {
	if [ "$(exe_of "$1")" = "$2" ]; then
		kill "-$3" "$1" 2>/dev/null || true
	fi
}

# Waits until pid is gone or limit seconds pass; fails when it is still there.
gone_within() {
	n=0
	while kill -0 "$1" 2>/dev/null; do
		[ "$n" -ge "$2" ] && return 1
		sleep 1
		n=$((n + 1))
	done
}

# Asks the installed app to quit and waits for its engine: an execute in
# flight finishes the action it is running. Only pids from the app's own
# pid file are signalled, and only while they still run the installed
# bundle's executables.
stop_running() {
	[ -f "$STATE/app.pid" ] || return 0
	app_pid=$(sed -n 1p "$STATE/app.pid" | tr -cd '0-9')
	engine_pid=$(sed -n 2p "$STATE/app.pid" | tr -cd '0-9')

	if [ -n "$app_pid" ] && [ "$(exe_of "$app_pid")" = "$DEST/Contents/MacOS/Regrow" ]; then
		echo "Quitting the running Regrow (pid $app_pid)."
		kill -TERM "$app_pid" 2>/dev/null || true
		if ! gone_within "$app_pid" "$QUIT_LIMIT"; then
			echo "Regrow (pid $app_pid) is still running after ${QUIT_LIMIT}s. Quit it from its menu, then run this again." >&2
			exit 1
		fi
	fi

	if [ -n "$engine_pid" ] && [ "$(exe_of "$engine_pid")" = "$DEST/Contents/MacOS/regrow-engine" ]; then
		if ! gone_within "$engine_pid" "$QUIT_LIMIT"; then
			echo "The engine (pid $engine_pid) is still running an action after ${QUIT_LIMIT}s; stopping it."
			engine="$DEST/Contents/MacOS/regrow-engine"
			signal_if "$engine_pid" "$engine" TERM
			if ! gone_within "$engine_pid" 5; then
				# The engine dies at once on its second SIGTERM.
				signal_if "$engine_pid" "$engine" TERM
				gone_within "$engine_pid" 2 || signal_if "$engine_pid" "$engine" KILL
			fi
		fi
	fi
}

cd "$ROOT"
if xcode-select -p 2>/dev/null | grep -q CommandLineTools; then
	echo "Toolchain: Command Line Tools ($(swift --version 2>&1 | sed -n 's/.*\(Swift version [0-9.]*\).*/\1/p' | head -1))"
fi

# The engine says `regrow 0.3.0`, as the release's CLI does; the bundle
# version is the plain number macOS compares bundles by.
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
version=${version#v}
short=$(printf '%s' "$version" | sed 's/-.*//')
case "$short" in
[0-9]*.[0-9]*) ;;
*) short=0.0.0 ;;
esac

# The module cache in app/.build keeps the folder it was built in: built
# again after the checkout moved, the app loses members of the SDK's types
# (`OperationQueue.main`). A cache not recorded as built here is cleaned.
here=$(pwd -P)
built_in="$ROOT/app/.build/built-in"
if [ -d "$ROOT/app/.build" ] && [ "$(cat "$built_in" 2>/dev/null)" != "$here" ]; then
	echo "app/.build holds no record of being built in this folder; cleaning it, so this build starts from scratch."
	swift package clean --package-path "$ROOT/app"
fi
mkdir -p "$ROOT/app/.build"
printf '%s\n' "$here" >"$built_in"

rm -rf "${BUNDLE:?}"
mkdir -p "$BUNDLE/Contents/MacOS" "$BUNDLE/Contents/Resources"
if [ "$mode" = release ]; then
	# A release runs on Intel Macs too: each executable holds both chips.
	parts="$ROOT/app/.build/parts"
	rm -rf "${parts:?}"
	mkdir -p "$parts"
	for arch in arm64 x86_64; do
		swift build -c release --package-path "$ROOT/app" --triple "$arch-apple-macosx14.0"
		bin=$(swift build -c release --package-path "$ROOT/app" --triple "$arch-apple-macosx14.0" --show-bin-path)
		cp "$bin/Regrow" "$parts/Regrow-$arch"
	done
	for arch in arm64 amd64; do
		CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-X main.version=$version" \
			-o "$parts/regrow-engine-$arch" ./cmd/regrow
	done
	lipo -create -output "$BUNDLE/Contents/MacOS/Regrow" "$parts/Regrow-arm64" "$parts/Regrow-x86_64"
	lipo -create -output "$BUNDLE/Contents/MacOS/regrow-engine" "$parts/regrow-engine-arm64" "$parts/regrow-engine-amd64"
else
	swift build -c release --package-path "$ROOT/app"
	bin=$(swift build -c release --package-path "$ROOT/app" --show-bin-path)
	cp "$bin/Regrow" "$BUNDLE/Contents/MacOS/Regrow"
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$version" \
		-o "$BUNDLE/Contents/MacOS/regrow-engine" ./cmd/regrow
fi
cp "$ROOT/app/Resources/page.html" "$BUNDLE/Contents/Resources/page.html"
cp -R "$ROOT/app/Resources/smoke" "$BUNDLE/Contents/Resources/smoke"
cp "$ROOT/app/Resources/Info.plist" "$BUNDLE/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $short" \
	-c "Set :CFBundleVersion $short" "$BUNDLE/Contents/Info.plist"

# The engine first: the bundle's signature seals it.
sign "$BUNDLE/Contents/MacOS/regrow-engine"
sign "$BUNDLE"
codesign --verify --strict "$BUNDLE"
echo "Built $BUNDLE ($version)"

if [ "$mode" = build ]; then
	echo "Ad-hoc signed: for a look, not for grants. \`scripts/app.sh install\` signs and installs it."
	exit 0
fi

codesign -d -r- "$BUNDLE" 2>&1 | sed -n 's/^designated => /Signed as: /p'
if [ "$mode" = signed ]; then
	echo "Signed as the installed app is, and not installed."
	exit 0
fi

if [ "$mode" = release ]; then
	# No extended attributes: the AppleDouble files that would carry them
	# land inside the bundle when another tool unpacks it, and break its seal.
	rm -rf "${DIST:?}"
	mkdir -p "$DIST"
	ditto -c -k --norsrc --noextattr --noqtn --noacl --keepParent "$BUNDLE" "$ZIP"
	unpacked=$(mktemp -d "${TMPDIR:-/tmp}/regrow-release.XXXXXX")
	ditto -x -k "$ZIP" "$unpacked"
	requirement="identifier \"com.github.lbagic.regrow\" and certificate leaf = H\"$identity\""
	if ! codesign --verify --strict --deep -R="$requirement" "$unpacked/Regrow.app"; then
		echo "The bundle unpacked from $ZIP does not verify as signed by $identity." >&2
		exit 1
	fi
	codesign -dvv "$unpacked/Regrow.app" 2>&1 | grep -E '^(Identifier|Format|Authority|TeamIdentifier)='
	echo "Executables: $(lipo -archs "$unpacked/Regrow.app/Contents/MacOS/Regrow"), engine $(lipo -archs "$unpacked/Regrow.app/Contents/MacOS/regrow-engine")"
	rm -rf "${unpacked:?}"
	echo "Packed $ZIP"
	exit 0
fi

stop_running

# Staged beside the destination, so the exchange stays on one volume.
mkdir -p "$HOME/Applications"
staged="$HOME/Applications/.Regrow.app.staged"
rm -rf "${staged:?}"
ditto "$BUNDLE" "$staged"
"$staged/Contents/MacOS/Regrow" --swap "$staged" "$DEST"
rm -rf "${staged:?}"

open "$DEST"
echo "Installed $DEST and launched it."
if [ "$check" = no ]; then
	exit 0
fi
if ! sh "$ROOT/scripts/app-smoke.sh" all "$DEST"; then
	echo "Installed, but a check failed: its lines above say which and where it left its files." >&2
	exit 1
fi
