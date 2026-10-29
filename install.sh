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
app_signer=0000000000000000000000000000000000000000

say() { printf '%s\n' "$*"; }
die() {
	printf 'regrow install: %s\n' "$*" >&2
	exit 1
}

fetch() {
	curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
		--output "$tmp/$1" "$release_url/$1" ||
		die "could not download $release_url/$1"
}

# Fails unless $tmp/$1 matches its line in the release's checksums.txt.
check_sum() {
	want=$(awk -v f="$1" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "checksums.txt has no line for $1; nothing was installed."
	got=$(shasum -a 256 "$tmp/$1" | awk '{ print $1 }')
	[ -n "$got" ] || die "shasum could not read $1; nothing was installed."
	[ "$got" = "$want" ] ||
		die "$1 does not match the release's checksums.txt; nothing was installed."
}

# The executable a pid runs, or nothing when the pid is gone.
exe_of() {
	ps -o comm= -p "$1" 2>/dev/null || true
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

# Asks a running Regrow to quit and waits for its engine to finish the
# action it is on, as scripts/app.sh does: only pids from the app's own
# pid file are signalled, and only while they run the installed bundle.
stop_app() {
	pids=${XDG_STATE_HOME:-$HOME/.local/state}/regrow/app.pid
	[ -f "$pids" ] || return 0
	app_pid=$(sed -n 1p "$pids" | tr -cd '0-9')
	engine_pid=$(sed -n 2p "$pids" | tr -cd '0-9')
	if [ -n "$app_pid" ] && [ "$(exe_of "$app_pid")" = "$app_dest/Contents/MacOS/Regrow" ]; then
		say "Quitting the running Regrow..."
		kill -TERM "$app_pid" 2>/dev/null || true
		gone_within "$app_pid" 45 ||
			die "Regrow is still running after 45 seconds; quit it from its menu and run this line again. $version was installed, Regrow.app was not."
	fi
	engine=$app_dest/Contents/MacOS/regrow-engine
	if [ -n "$engine_pid" ] && [ "$(exe_of "$engine_pid")" = "$engine" ]; then
		if ! gone_within "$engine_pid" 45; then
			say "Regrow's engine is still running an action after 45 seconds; stopping it."
			if [ "$(exe_of "$engine_pid")" = "$engine" ]; then
				kill -TERM "$engine_pid" 2>/dev/null || true
			fi
			gone_within "$engine_pid" 5 ||
				die "Regrow's engine (pid $engine_pid) did not stop; run this line again once it has. $version was installed, Regrow.app was not."
		fi
	fi
}

# Puts the checked bundle in $tmp/app in ~/Applications in one step,
# through the new bundle's own --swap, and opens it.
install_app() {
	staged=$HOME/Applications/.Regrow.app.staged
	failed="$version was installed, Regrow.app was not"
	mkdir -p "$HOME/Applications" || die "could not create ~/Applications; $failed."
	rm -rf "${staged:?}" || die "could not clear $staged; $failed."
	ditto "$tmp/app/Regrow.app" "$staged" || die "could not copy Regrow.app to ~/Applications; $failed."
	stop_app
	"$staged/Contents/MacOS/Regrow" --swap "$staged" "$app_dest" ||
		die "could not put Regrow.app in ~/Applications; $failed."
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
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	say "Downloading regrow for $chip..."
	fetch "$asset"
	fetch checksums.txt
	check_sum "$asset"

	tar -xzf "$tmp/$asset" -C "$tmp" regrow ||
		die "could not unpack $asset; nothing was installed."
	chmod 755 "$tmp/regrow"
	version=$("$tmp/regrow" version) ||
		die "the downloaded regrow does not run on this Mac; nothing was installed."

	app=yes
	if [ "${macos%%.*}" -lt 14 ]; then
		app="Regrow.app, the menu bar app, needs macOS 14 or later; this Mac has macOS $macos"
	elif ! awk -v f="$app_zip" '$2 == f { found = 1 } END { exit !found }' "$tmp/checksums.txt"; then
		app="The latest release has no Regrow.app"
	fi
	if [ "$app" = yes ]; then
		say "Downloading Regrow.app..."
		fetch "$app_zip"
		check_sum "$app_zip"
		mkdir "$tmp/app"
		ditto -x -k "$tmp/$app_zip" "$tmp/app" ||
			die "could not unpack $app_zip; nothing was installed."
		codesign --verify --strict --deep \
			-R="identifier \"com.github.lbagic.regrow\" and certificate leaf = H\"$app_signer\"" \
			"$tmp/app/Regrow.app" 2>/dev/null ||
			die "Regrow.app in the release is not signed as regrow's releases are; nothing was installed."
	fi

	mkdir -p "$bin_dir" || die "could not create ~/.local/bin; nothing was installed."
	mv -f "$tmp/regrow" "$bin_dir/regrow" || die "could not write ~/.local/bin/regrow."
	say "Installed $version to ~/.local/bin/regrow."
	if [ "$app" = yes ]; then
		install_app
	else
		say "$app, so only the command line tool was installed."
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
	say "Next, run:  $next"
	say "It shows what fills your disk and removes nothing until you confirm."
}

main "$@"
