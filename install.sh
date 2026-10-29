#!/bin/sh
# Installs regrow on macOS, and on macOS 14 or later Regrow.app, its menu
# bar app, in ~/Applications:
#   curl -fsSL https://raw.githubusercontent.com/lbagic/regrow/main/install.sh | sh
#
# Asset names carry no version (.goreleaser.yaml's archive name_template),
# so this script fetches every release from releases/latest unchanged.

# shellcheck disable=SC2088 # a ~ in a message is shown, not expanded
set -eu

release_url=https://github.com/lbagic/regrow/releases/latest/download
app_zip=Regrow.app.zip
# The SHA-1 of the certificate releases sign Regrow.app with, written by
# `scripts/signing-cert.sh --release`; the release workflow signs with
# no other. macOS keeps the app's grants for this signature.
app_signer=E941D3CB4CBB3FBFB2FB78C65C7EB0F0CB47C2C8

say() { printf '%s\n' "$*"; }
die() {
	printf 'regrow install: %s\n' "$*" >&2
	exit 1
}

# Downloads $1 into $tmp.
get() {
	curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
		--output "$tmp/$1" "$release_url/$1"
}

fetch() {
	get "$1" || die "could not download $release_url/$1"
}

# Whether $tmp/$1 matches its line in the release's checksums.txt; when
# it does not, says why.
sum_matches() {
	want=$(awk -v f="$1" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || { say "checksums.txt has no line for $1"; return 1; }
	got=$(shasum -a 256 "$tmp/$1" | awk '{ print $1 }')
	[ -n "$got" ] || { say "shasum could not read $1"; return 1; }
	[ "$got" = "$want" ] || { say "$1 does not match the release's checksums.txt"; return 1; }
}

# Each pid running the executable at path $1.
pids_of() {
	ps -axo pid=,comm= | awk -v want="$1" '{ line = $0; sub(/^ *[0-9]+ /, "", line); if (line == want) print $1 }'
}

# Signals pid $1 with $3 only while it still runs the executable $2: a
# pid that has gone may have passed to another program.
signal_if() {
	if [ "$(ps -o comm= -p "$1" 2>/dev/null || true)" = "$2" ]; then
		kill "-$3" "$1" 2>/dev/null || true
	fi
}

# Waits until pid $1 is gone or $2 seconds pass; fails when it is still there.
gone_within() {
	n=0
	while kill -0 "$1" 2>/dev/null; do
		[ "$n" -ge "$2" ] && return 1
		sleep 1
		n=$((n + 1))
	done
}

# Downloads Regrow.app into $tmp/app and checks its checksum and its
# signer; on a failure, sets app_note to why.
check_app() {
	say "Downloading Regrow.app..."
	if ! get "$app_zip"; then
		app_note="could not download $release_url/$app_zip"
		return 1
	fi
	app_note=$(sum_matches "$app_zip") || return 1
	mkdir "$tmp/app"
	if ! ditto -x -k "$tmp/$app_zip" "$tmp/app"; then
		app_note="could not unpack $app_zip"
		return 1
	fi
	if ! codesign --verify --strict --deep \
		-R="identifier \"com.github.lbagic.regrow\" and certificate leaf = H\"$app_signer\"" \
		"$tmp/app/Regrow.app" 2>/dev/null; then
		app_note="the release's Regrow.app is not signed as regrow's releases are"
		return 1
	fi
}

# Quits every Regrow running the installed bundle, as its menu's Quit
# does, then stops an engine of it still running, as scripts/app.sh
# does; on a failure, sets app_note to why.
stop_app() {
	app_exe=$app_dest/Contents/MacOS/Regrow
	engine=$app_dest/Contents/MacOS/regrow-engine
	for pid in $(pids_of "$app_exe"); do
		say "Quitting the running Regrow..."
		signal_if "$pid" "$app_exe" TERM
		quit_app=yes
		if ! gone_within "$pid" 45; then
			app_note="Regrow is still running after 45 seconds; quit it from its menu and run this line again"
			return 1
		fi
	done
	for pid in $(pids_of "$engine"); do
		gone_within "$pid" 45 && continue
		say "Regrow's engine is still running an action after 45 seconds; stopping it."
		signal_if "$pid" "$engine" TERM
		gone_within "$pid" 5 && continue
		# The engine stops at once on its second SIGTERM.
		signal_if "$pid" "$engine" TERM
		gone_within "$pid" 2 && continue
		signal_if "$pid" "$engine" KILL
		if ! gone_within "$pid" 2; then
			app_note="Regrow's engine (pid $pid) did not stop; run this line again once it has"
			return 1
		fi
	done
}

# Puts the checked bundle in $tmp/app in ~/Applications in one step,
# through the new bundle's own --swap, and opens it; on a failure, sets
# app_note to why and leaves no staged copy.
install_app() {
	staging=yes
	if ! mkdir -p "$HOME/Applications" || ! rm -rf "${staged:?}" || ! ditto "$tmp/app/Regrow.app" "$staged"; then
		app_note="could not copy it to ~/Applications"
		rm -rf "${staged:?}" || true
		return 1
	fi
	if ! stop_app; then
		rm -rf "${staged:?}" || true
		return 1
	fi
	if ! "$staged/Contents/MacOS/Regrow" --swap "$staged" "$app_dest"; then
		app_note="could not put it in ~/Applications"
		rm -rf "${staged:?}" || true
		if [ "$quit_app" = yes ]; then
			open "$app_dest" 2>/dev/null || true
		fi
		return 1
	fi
	# After a swap the old bundle is here.
	rm -rf "${staged:?}" || true
	app_version=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app_dest/Contents/Info.plist" 2>/dev/null || true)
	say "Installed Regrow.app $app_version to ~/Applications."
	if open "$app_dest" 2>/dev/null; then
		say "Regrow is in your menu bar. For a full scan, give it Full Disk Access in System Settings > Privacy & Security."
	else
		say "Open Regrow from ~/Applications to start it."
	fi
}

# Everything runs from main, called on the last line, so a download of
# this script cut short runs nothing.
main() {
	os=$(uname -s)
	[ "$os" = Darwin ] || die "regrow runs on macOS only; this system is $os."
	machine=$(uname -m)
	case $machine in
	arm64) arch=arm64 chip="Apple silicon" ;;
	x86_64) arch=amd64 chip=Intel ;;
	*) die "there is no regrow build for this Mac's chip ($machine)." ;;
	esac
	macos=$(sw_vers -productVersion 2>/dev/null || true)
	[ "${macos%%.*}" -ge 12 ] 2>/dev/null ||
		die "regrow needs macOS 12 or later; this Mac has macOS $macos."
	asset=regrow_darwin_$arch.tar.gz
	bin_dir=$HOME/.local/bin
	app_dest=$HOME/Applications/Regrow.app

	tmp=$(mktemp -d)
	# Before the swap the staged copy is the new app, after it the old one.
	staged=$HOME/Applications/.Regrow.app.staged staging=no quit_app=no
	trap 'rm -rf "$tmp"; if [ "$staging" = yes ]; then rm -rf "$staged"; fi' EXIT
	trap 'exit 1' HUP INT TERM

	say "Downloading regrow for $chip..."
	fetch "$asset"
	fetch checksums.txt
	why=$(sum_matches "$asset") || die "$why; nothing was installed."

	tar -xzf "$tmp/$asset" -C "$tmp" regrow ||
		die "could not unpack $asset; nothing was installed."
	chmod 755 "$tmp/regrow"
	version=$("$tmp/regrow" version) ||
		die "the downloaded regrow does not run on this Mac; nothing was installed."

	# app: yes to install it, skip when this Mac or release has none,
	# failed when it could not be installed, with app_note saying why.
	app=yes app_note=
	if [ "${macos%%.*}" -lt 14 ]; then
		app=skip app_note="Regrow.app, the menu bar app, needs macOS 14 or later; this Mac has macOS $macos, so only the command line tool was installed."
	elif ! awk -v f="$app_zip" '$2 == f { found = 1 } END { exit !found }' "$tmp/checksums.txt"; then
		app=skip app_note="The latest release has no Regrow.app, so only the command line tool was installed."
	elif ! check_app; then
		app=failed
	fi

	mkdir -p "$bin_dir" || die "could not create ~/.local/bin; nothing was installed."
	mv -f "$tmp/regrow" "$bin_dir/regrow" || die "could not write ~/.local/bin/regrow."
	say "Installed $version to ~/.local/bin/regrow."
	if [ "$app" = yes ] && ! install_app; then
		app=failed
	fi

	next='~/.local/bin/regrow'
	case :$PATH: in
	*:"$bin_dir":*)
		next=regrow
		;;
	*)
		# shellcheck disable=SC2016 # expanded by the shell that reads the line
		line='export PATH="$HOME/.local/bin:$PATH"'
		rc=
		case ${SHELL:-} in
		*/zsh) rc=.zprofile ;;
		*/bash)
			# A bash login shell reads only the first of these that exists.
			for rc in .bash_profile .bash_login .profile; do
				if [ -e "$HOME/$rc" ]; then break; fi
			done
			[ -e "$HOME/$rc" ] || rc=.bash_profile
			;;
		esac
		if [ -z "$rc" ]; then
			say "~/.local/bin is not on your PATH. Add this line to your shell's startup file:"
			say "  $line"
		else
			if [ -f "$HOME/$rc" ] && grep -qxF "$line" "$HOME/$rc"; then
				say "~/$rc already adds ~/.local/bin to PATH."
			else
				say "Adding ~/.local/bin to PATH in ~/$rc:"
				say "  $line"
				printf '\n%s\n' "$line" >>"$HOME/$rc" ||
					die "could not write ~/$rc; add the line above to it yourself."
			fi
			next='regrow  in a new Terminal window, or  ~/.local/bin/regrow  in this one'
		fi
		;;
	esac
	found=$(command -v regrow || true)
	if [ -n "$found" ] && [ "$found" != "$bin_dir/regrow" ]; then
		say "Another regrow, at $found, is on your PATH and may run instead of this one; remove it, or run ~/.local/bin/regrow."
		next='~/.local/bin/regrow'
	fi
	case $app in
	skip) say "$app_note" ;;
	failed) printf 'regrow install: Regrow.app was not installed: %s.\n' "$app_note" >&2 ;;
	esac
	say "Next, run:  $next"
	say "It shows what fills your disk and removes nothing until you confirm."
	[ "$app" != failed ]
}

main "$@"
