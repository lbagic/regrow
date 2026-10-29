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

	tar -xzf "$tmp/$asset" -C "$tmp" regrow || die "$asset holds no regrow binary."
	chmod 755 "$tmp/regrow"
	version=$("$tmp/regrow" version) ||
		die "the downloaded regrow does not run on this Mac (it needs macOS 12 or later); nothing was installed."
	mkdir -p "$bin_dir" || die "could not create ~/.local/bin."
	mv -f "$tmp/regrow" "$bin_dir/regrow" || die "could not write ~/.local/bin/regrow."
	say "Installed $version to ~/.local/bin/regrow."

	case :$PATH: in
	*:"$bin_dir":*)
		found=$(command -v regrow || true)
		if [ "$found" = "$bin_dir/regrow" ]; then
			say "Next, run:  regrow"
		else
			say "Your PATH finds another regrow first, at $found."
			say "Next, run:  ~/.local/bin/regrow"
		fi
		;;
	*)
		# shellcheck disable=SC2016 # expanded by the shell that reads the line
		line='export PATH="$HOME/.local/bin:$PATH"'
		profile=$HOME/.zprofile
		case ${SHELL:-} in
		*/zsh)
			if [ -f "$profile" ] && grep -qxF "$line" "$profile"; then
				say "~/.zprofile already adds ~/.local/bin to PATH."
			else
				say "Adding ~/.local/bin to PATH in ~/.zprofile:"
				say "  $line"
				printf '\n%s\n' "$line" >>"$profile" ||
					die "could not write ~/.zprofile; add the line above to it yourself."
			fi
			say "Next, run:  regrow  in a new Terminal window, or  ~/.local/bin/regrow  in this one."
			;;
		*)
			case ${SHELL:-} in
			*/bash) rc='~/.bash_profile' ;;
			*) rc="your shell's startup file" ;;
			esac
			say "~/.local/bin is not on your PATH. Add this line to $rc:"
			say "  $line"
			say "Next, run:  ~/.local/bin/regrow"
			;;
		esac
		;;
	esac
	say "It shows what fills your disk and removes nothing until you confirm."
}

main "$@"
