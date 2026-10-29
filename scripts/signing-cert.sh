#!/bin/sh
# Makes the self-signed code-signing identity Regrow.app is signed with,
# and records its SHA-1 in .signing-identity, which `just app` signs by.
# macOS ties Full Disk Access and the other grants to the signature, so
# one identity kept across builds keeps them.
#
#   scripts/signing-cert.sh            record "regrow release signing" when the
#                                      keychain holds it, else find or create
#                                      "regrow local signing", for this Mac alone
#   scripts/signing-cert.sh --release  create "regrow release signing", which CI
#                                      signs each release's app with: store it in
#                                      the repository's Actions secrets, import it
#                                      here, record it and pin it in install.sh
#   --dry-run, with either             say what would happen; the keychain is only read
#
# The first signature with a new key raises a keychain dialog: choose
# "Always Allow". A re-run creates nothing and prints the same hash.
set -eu

LOCAL="regrow local signing"
RELEASE="regrow release signing"
REPO=lbagic/regrow
DAYS=3653
ROOT=$(cd "$(dirname "$0")/.." && pwd)
RECORD="$ROOT/.signing-identity"
INSTALLER="$ROOT/install.sh"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"
OPENSSL=/usr/bin/openssl

dry=0
release=0
for arg in "$@"; do
	case "$arg" in
	--dry-run) dry=1 ;;
	--release) release=1 ;;
	*)
		echo "usage: $0 [--release] [--dry-run]" >&2
		exit 64
		;;
	esac
done

# SHA-1 of every certificate in the keychain named $1.
existing() {
	security find-certificate -a -c "$1" -Z "$KEYCHAIN" 2>/dev/null |
		awk -v want="\"alis\"<blob>=\"$1\"" '
			/^SHA-1 hash:/ { hash = $3 }
			index($0, want) { print hash }'
}

# Whether the keychain holds the private key too: a certificate alone signs nothing.
has_key() {
	security find-identity -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "$1"
}

# The SHA-1 of the identity named $1, or nothing when the keychain has
# none. Fails on two certificates of that name, or one without its key.
identity() {
	found=$(existing "$1")
	count=$(printf '%s' "$found" | grep -c . || true)
	if [ "$count" -gt 1 ]; then
		echo "The keychain holds $count certificates named \"$1\":" >&2
		printf '  %s\n' $found >&2
		echo "Delete all but one in Keychain Access (login, My Certificates), then run this again." >&2
		return 1
	fi
	if [ "$count" -eq 1 ] && ! has_key "$found"; then
		echo "A certificate named \"$1\" exists ($found) but its private key is not in the keychain." >&2
		echo "Delete it in Keychain Access, then run this again." >&2
		return 1
	fi
	printf '%s' "$found"
}

record() {
	if [ "$dry" -eq 1 ]; then
		echo "dry run: would write $1 to $RECORD"
		return
	fi
	printf '%s\n' "$1" >"$RECORD"
}

# The one app_signer= line install.sh checks the app it installs against.
pinned_line() {
	n=$(grep -c '^app_signer=' "$INSTALLER" || true)
	if [ "$n" -ne 1 ]; then
		echo "$INSTALLER has $n lines starting app_signer=, not one." >&2
		return 1
	fi
}

# Makes a key and a certificate named $1 in $work and sets sha1 to the
# certificate's, then packs them in $work/identity.p12 under $pass.
make_identity() {
	cat >"$work/req.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $1
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
	"$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days "$DAYS" -config "$work/req.cnf" \
		-keyout "$work/key.pem" -out "$work/cert.pem" 2>/dev/null
	sha1=$("$OPENSSL" x509 -in "$work/cert.pem" -noout -fingerprint -sha1 | sed 's/.*=//; s/://g')
	pass=$("$OPENSSL" rand -hex 32)
	"$OPENSSL" pkcs12 -export -name "$1" -inkey "$work/key.pem" -in "$work/cert.pem" \
		-out "$work/identity.p12" -passout "pass:$pass"
}

# Imports $work/identity.p12, then signs a scratch file with it, while
# someone is watching for the keychain dialog.
import_identity() {
	security import "$work/identity.p12" -k "$KEYCHAIN" -P "$pass" -T /usr/bin/codesign >/dev/null
	if ! has_key "$sha1"; then
		echo "The import ran, but the keychain does not list $sha1 as a signing identity." >&2
		exit 1
	fi
	cp /usr/bin/true "$work/probe"
	echo "Signing a scratch file with the new key. If a keychain dialog appears, choose \"Always Allow\"."
	if ! codesign --force --sign "$sha1" "$work/probe"; then
		echo "codesign could not use the new identity ($sha1). It stays in the keychain; nothing was recorded." >&2
		exit 1
	fi
}

if [ "$release" -eq 0 ]; then
	found=$(identity "$RELEASE")
	if [ -n "$found" ]; then
		record "$found"
		echo "$found"
		echo "The release identity \"$RELEASE\" is in the keychain: builds here sign as releases do."
		exit 0
	fi
	found=$(identity "$LOCAL")
	if [ -n "$found" ]; then
		record "$found"
		echo "$found"
		echo "The identity \"$LOCAL\" already exists; nothing was created."
		exit 0
	fi
else
	found=$(identity "$RELEASE")
	if [ -n "$found" ]; then
		echo "The keychain already holds \"$RELEASE\" ($found); nothing was created." >&2
		echo "A new one changes the signature every installed Regrow.app holds its grants by. To make one anyway, delete this one in Keychain Access (login, My Certificates), then run this again." >&2
		exit 1
	fi
	pinned_line
	if ! gh secret list -R "$REPO" >/dev/null; then
		echo "gh cannot list $REPO's secrets, so it could not set them; run \`gh auth login\` as the repository's owner, then this again." >&2
		exit 1
	fi
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/regrow-signing.XXXXXX")
trap 'rm -rf "${work:?}"' EXIT
umask 077

if [ "$release" -eq 0 ]; then
	make_identity "$LOCAL"
	if [ "$dry" -eq 1 ]; then
		echo "dry run: no identity named \"$LOCAL\" in $KEYCHAIN."
		echo "dry run: a throwaway certificate was made to check openssl, and is deleted now:"
		"$OPENSSL" x509 -in "$work/cert.pem" -noout -subject -enddate -text |
			grep -E 'subject=|notAfter=|Code Signing|Digital Signature|CA:FALSE' | sed 's/^ */  /'
		echo "dry run: would import a new one with: security import <p12> -k $KEYCHAIN -T /usr/bin/codesign"
		record "<its SHA-1>"
		exit 0
	fi
	import_identity
	record "$sha1"
	echo "$sha1"
	echo "Created \"$LOCAL\", valid $DAYS days, and recorded it in $RECORD."
	exit 0
fi

make_identity "$RELEASE"
if [ "$dry" -eq 1 ]; then
	"$OPENSSL" pkcs12 -in "$work/identity.p12" -nokeys -passin "pass:$pass" 2>/dev/null |
		"$OPENSSL" x509 -noout -subject -enddate -fingerprint -sha1 | sed 's/^/  /'
	echo "dry run: a throwaway identity was made, packed and read back, and is deleted now."
	echo "dry run: would store it in $REPO's Actions secrets REGROW_SIGNING_P12 and REGROW_SIGNING_PASSWORD,"
	echo "dry run: import it with: security import <p12> -k $KEYCHAIN -T /usr/bin/codesign"
	record "<its SHA-1>"
	echo "dry run: would set app_signer=<its SHA-1> in $INSTALLER"
	exit 0
fi

# The secrets first: they take a new value on every run, while an
# identity in the keychain stops the next run.
base64 -i "$work/identity.p12" | gh secret set REGROW_SIGNING_P12 -R "$REPO"
printf '%s' "$pass" | gh secret set REGROW_SIGNING_PASSWORD -R "$REPO"
import_identity
record "$sha1"
sed "s/^app_signer=.*/app_signer=$sha1/" "$INSTALLER" >"$work/install.sh"
cat "$work/install.sh" >"$INSTALLER"
echo "$sha1"
echo "Created \"$RELEASE\", valid $DAYS days: stored in $REPO's Actions secrets, imported into the login keychain, recorded in $RECORD and pinned in $INSTALLER."
echo "Commit install.sh: the release workflow signs only with the identity it pins."
