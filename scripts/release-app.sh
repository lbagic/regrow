#!/bin/sh
# Builds the release's app/.build/dist/Regrow.app.zip on a CI runner.
# Imports the release identity from REGROW_SIGNING_P12 (the .p12 that
# `scripts/signing-cert.sh --release` stored, base64) and
# REGROW_SIGNING_PASSWORD into a keychain of its own, refuses an identity
# other than the one install.sh pins, runs `scripts/app.sh release`, and
# deletes the keychain however it ends.
#
#   scripts/release-app.sh               sign with the release identity
#   scripts/release-app.sh --throwaway   sign with an identity made here, for a dry run
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
NAME="regrow release signing"
OPENSSL=/usr/bin/openssl

die() {
	echo "release-app: $*" >&2
	exit 1
}

throwaway=0
case "${1:-}" in
"") ;;
--throwaway) throwaway=1 ;;
*)
	echo "usage: $0 [--throwaway]" >&2
	exit 64
	;;
esac

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/regrow-release.XXXXXX")
keychain="$work/release.keychain-db"
searched=$(security list-keychains -d user | sed 's/^ *"//; s/"$//')
cleanup() {
	# shellcheck disable=SC2086 # one keychain path per word
	security list-keychains -d user -s $searched || true
	security delete-keychain "$keychain" 2>/dev/null || true
	rm -rf "${work:?}"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
umask 077

if [ "$throwaway" -eq 1 ]; then
	cat >"$work/req.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
	"$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days 1 -config "$work/req.cnf" \
		-keyout "$work/key.pem" -out "$work/cert.pem" 2>/dev/null
	REGROW_SIGNING_PASSWORD=$("$OPENSSL" rand -hex 32)
	"$OPENSSL" pkcs12 -export -name "$NAME" -inkey "$work/key.pem" -in "$work/cert.pem" \
		-out "$work/identity.p12" -passout "pass:$REGROW_SIGNING_PASSWORD"
	rm -f "$work/key.pem"
	pinned=$("$OPENSSL" x509 -in "$work/cert.pem" -noout -fingerprint -sha1 | sed 's/.*=//; s/://g')
	echo "Dry run: signing with a throwaway identity, $pinned."
else
	[ -n "${REGROW_SIGNING_P12:-}" ] || die "REGROW_SIGNING_P12 is not set: run scripts/signing-cert.sh --release once, which stores it in the release environment's secrets."
	[ -n "${REGROW_SIGNING_PASSWORD:-}" ] || die "REGROW_SIGNING_PASSWORD is not set: run scripts/signing-cert.sh --release once, which stores it in the release environment's secrets."
	pinned=$(sed -n 's/^app_signer=//p' "$ROOT/install.sh")
	[ -n "$pinned" ] || die "install.sh pins no app_signer."
	printf '%s' "$REGROW_SIGNING_P12" | base64 -D -o "$work/identity.p12"
fi

secret=$("$OPENSSL" rand -hex 32)
security create-keychain -p "$secret" "$keychain"
security set-keychain-settings -lut 3600 "$keychain"
security unlock-keychain -p "$secret" "$keychain"
security import "$work/identity.p12" -k "$keychain" -P "$REGROW_SIGNING_PASSWORD" -T /usr/bin/codesign >/dev/null
rm -f "$work/identity.p12"
# Nothing the build starts gets the key: it is in the keychain now.
unset REGROW_SIGNING_P12 REGROW_SIGNING_PASSWORD
# codesign uses the key without a dialog, which nobody would answer here.
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$secret" "$keychain" >/dev/null
# shellcheck disable=SC2086 # one keychain path per word
security list-keychains -d user -s "$keychain" $searched

sha1=$(security find-certificate -a -c "$NAME" -Z "$keychain" | awk '/^SHA-1 hash:/ { print $3 }')
[ -n "$sha1" ] || die "the identity holds no certificate named \"$NAME\"."
upper() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }
[ "$(upper "$sha1")" = "$(upper "$pinned")" ] ||
	die "the secret REGROW_SIGNING_P12 holds $sha1, but install.sh pins $pinned: an app signed by it would lose every grant on every Mac that installed Regrow.app."

printf '%s\n' "$sha1" >"$ROOT/.signing-identity"
sh "$ROOT/scripts/app.sh" release
