#!/usr/bin/env bash
# Downloads the zapret release this project is pinned to, verifies every file
# against the release's sha256sum.txt and lays the engine binaries out per
# GOOS_GOARCH so they can be embedded into the prosvet binary.
#
#   scripts/fetch-engine.sh            # uses the version in internal/engine/bundle/VERSION
#   scripts/fetch-engine.sh v72.14     # bump to another release
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUNDLE="$ROOT/internal/engine/bundle"
VERSION="${1:-$(cat "$BUNDLE/VERSION" 2>/dev/null || echo v72.13)}"
BASE="https://github.com/bol-van/zapret/releases/download/$VERSION"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "zapret $VERSION"
curl -fsSL "$BASE/zapret-$VERSION.tar.gz" -o "$WORK/z.tgz"
curl -fsSL "$BASE/sha256sum.txt" -o "$WORK/sha256sum.txt"
mkdir "$WORK/x"
tar -xzf "$WORK/z.tgz" -C "$WORK/x"
SRC="$WORK/x/zapret-$VERSION"

verify() { # verify <path relative to the archive root>
	local rel="zapret-$VERSION/$1"
	local want got
	want="$(awk -v f="$rel" '$2==f || $2=="*"f {print $1}' "$WORK/sha256sum.txt")"
	got="$(sha256sum "$WORK/x/$rel" | awk '{print $1}')"
	if [ -z "$want" ] || [ "$want" != "$got" ]; then
		echo "checksum mismatch for $1" >&2
		exit 1
	fi
}

put() { # put <src rel> <dest dir> [dest name]
	# The release checksum list covers executables only.
	case "$1" in binaries/*) verify "$1" ;; esac
	mkdir -p "$BUNDLE/$2"
	cp "$SRC/$1" "$BUNDLE/$2/${3:-$(basename "$1")}"
}

rm -rf "$BUNDLE"/linux_* "$BUNDLE"/windows_* "$BUNDLE/fake"
put binaries/linux-x86_64/nfqws linux_amd64
put binaries/linux-x86/nfqws linux_386
put binaries/linux-arm64/nfqws linux_arm64
put binaries/linux-arm/nfqws linux_arm
put binaries/linux-mips/nfqws linux_mips
put binaries/linux-mipsel/nfqws linux_mipsle
for f in winws.exe WinDivert.dll WinDivert64.sys cygwin1.dll; do
	put "binaries/windows-x86_64/$f" windows_amd64
done
for f in winws.exe WinDivert.dll WinDivert32.sys cygwin1.dll; do
	put "binaries/windows-x86/$f" windows_386
done
for f in stun.bin quic_initial_www_google_com.bin tls_clienthello_www_google_com.bin \
	tls_clienthello_iana_org.bin http_iana_org.bin discord-ip-discovery-with-port.bin \
	discord-ip-discovery-without-port.bin zero_256.bin; do
	put "files/fake/$f" fake
done
cp "$SRC/docs/LICENSE.txt" "$BUNDLE/LICENSE.zapret.txt"
echo "$VERSION" >"$BUNDLE/VERSION"

# Manifest used at runtime to detect tampering (antivirus "cleaning", etc).
(cd "$BUNDLE" && find . -type f ! -name MANIFEST.sha256 ! -name VERSION ! -name '*.txt' ! -name '*.go' -print0 |
	sort -z | xargs -0 sha256sum | sed 's#  \./#  #') >"$BUNDLE/MANIFEST.sha256"
echo "bundle ready: $(wc -l <"$BUNDLE/MANIFEST.sha256") files"
