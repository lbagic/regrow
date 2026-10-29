#!/bin/sh
# Installs regrow on macOS:
#   curl -fsSL https://raw.githubusercontent.com/lbagic/regrow/main/install.sh | sh
#
# Asset names carry no version (.goreleaser.yaml's archive name_template),
# so this script fetches every release from releases/latest unchanged.

# shellcheck disable=SC2088 # a ~ in a message is shown, not expanded
set -eu

release_url=https://github.com/lbagic/regrow/releases/latest/download

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

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	say "Downloading regrow for $chip..."
	fetch "$asset"
	fetch checksums.txt
	want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "checksums.txt has no line for $asset; nothing was installed."
	got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
	[ -n "$got" ] || die "shasum could not read $asset; nothing was installed."
	[ "$got" = "$want" ] ||
		die "$asset does not match the release's checksums.txt; nothing was installed."

	tar -xzf "$tmp/$asset" -C "$tmp" regrow ||
		die "could not unpack $asset; nothing was installed."
	chmod 755 "$tmp/regrow"
	version=$("$tmp/regrow" version) ||
		die "the downloaded regrow does not run on this Mac; nothing was installed."
	mkdir -p "$bin_dir" || die "could not create ~/.local/bin; nothing was installed."
	mv -f "$tmp/regrow" "$bin_dir/regrow" || die "could not write ~/.local/bin/regrow."
	say "Installed $version to ~/.local/bin/regrow."

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
