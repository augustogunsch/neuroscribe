#!/bin/sh
#
# Fetch one runtime file, and install it only if it is byte-for-byte what
# assets.sha256 says it should be.
#
# The files this handles are not libraries the server links against — they are
# a Python interpreter and a typesetter that execute in the reader's browser,
# downloaded from a CDN, an npm registry and a git branch that can all change
# under us. The pin is what makes that acceptable. A file already on disk is
# re-verified rather than skipped, so `make assets` doubles as an integrity
# check of a deployment.
#
#   usage: scripts/fetch.sh <url> <destination>

set -eu

manifest=${MANIFEST:-assets.sha256}
url=$1
dest=$2

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	fi
}

# The manifest names each file by the path the browser fetches it at, which is
# also the path it is embedded under — "static/vendor/purify.min.js". Where the
# repository happens to keep that file is a separate question, and the answer
# is under web/. Stripping the prefix here keeps the pin tied to the identity
# that matters rather than to a directory layout.
key=${dest#web/}

expected=$(awk -v want="$key" '$1 !~ /^#/ && $2 == want { print $1 }' "$manifest")
if [ -z "$expected" ]; then
	echo "$key: no checksum pinned in $manifest" >&2
	echo "  add one deliberately — this file ends up running in a browser" >&2
	exit 1
fi

check() {
	got=$(sha256_of "$1")
	if [ "$got" = "$expected" ]; then
		return 0
	fi
	echo "$dest: checksum mismatch" >&2
	echo "  expected $expected" >&2
	echo "  got      $got" >&2
	return 1
}

if [ -f "$dest" ]; then
	if check "$dest"; then
		exit 0
	fi
	echo "  the copy on disk is not the pinned one; delete it to re-fetch" >&2
	exit 1
fi

mkdir -p "$(dirname "$dest")"
tmp="$dest.part"
trap 'rm -f "$tmp"' EXIT INT TERM

echo "fetching $dest"
curl -sSfL -o "$tmp" "$url"

# Verified before it is given its real name, so a bad download is never
# extracted, served, or mistaken for a complete one on the next run.
if ! check "$tmp"; then
	echo "  refusing to install it" >&2
	exit 1
fi
mv "$tmp" "$dest"
