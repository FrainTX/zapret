#!/usr/bin/env bash
# Собирает все релизные артефакты в dist/.
#
#   scripts/build.sh            # всё, что можно собрать на этой машине
#   scripts/build.sh linux      # только Linux
#   scripts/build.sh windows    # только Windows
#
# Go берётся из PATH, иначе скачивается в .toolchain/ (с проверкой SHA256).
# Всё собирается без cgo: один статический файл на платформу.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
WHAT="${1:-all}"
GO_VERSION="1.27.2"

ensure_go() {
	if [ -x "$ROOT/.toolchain/go/bin/go" ]; then
		. "$ROOT/.toolchain/env.sh"
		return
	fi
	if command -v go >/dev/null 2>&1; then
		return
	fi
	echo "Go не найден — скачиваю go$GO_VERSION в .toolchain/"
	mkdir -p "$ROOT/.toolchain"
	local tgz="go$GO_VERSION.linux-amd64.tar.gz"
	curl -fsSL "https://go.dev/dl/$tgz" -o "$ROOT/.toolchain/$tgz"
	local want got
	want="$(curl -fsSL "https://go.dev/dl/?mode=json" | python3 -c "import json,sys;print(next(f['sha256'] for r in json.load(sys.stdin) for f in r['files'] if f['filename']=='$tgz'))")"
	got="$(sha256sum "$ROOT/.toolchain/$tgz" | awk '{print $1}')"
	[ "$want" = "$got" ] || { echo "контрольная сумма Go не совпала" >&2; exit 1; }
	tar -xzf "$ROOT/.toolchain/$tgz" -C "$ROOT/.toolchain" && rm "$ROOT/.toolchain/$tgz"
	. "$ROOT/.toolchain/env.sh"
}
ensure_go

VERSION="${PROSVET_VERSION:-$(sed -n 's/^var Version = "\(.*\)"/\1/p' internal/core/core.go)}"
# Репозиторий, откуда установленные копии берут обновления: из окружения
# (CI) или из git remote origin. Пусто — сборка не обновляется сама.
REPO="${PROSVET_REPO:-$(git remote get-url origin 2>/dev/null | sed -E 's#^(git@github\.com:|https://github\.com/)##; s#\.git$##')}"
LDFLAGS="-s -w -X prosvet/internal/core.Version=$VERSION -X prosvet/internal/update.Repo=$REPO"
DIST="$ROOT/dist"
rm -rf "$DIST"
mkdir -p "$DIST"

echo "Просвет $VERSION"
go vet ./internal/... ./cmd/prosvet
go test ./internal/... >/dev/null
echo "  тесты пройдены"

build_linux() {
	for arch in amd64 arm64 386 arm mips mipsle; do
		out="$DIST/prosvet-$VERSION-linux-$arch"
		mkdir -p "$out"
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$out/prosvet" ./cmd/prosvet
		echo "  linux/$arch  prosvet"
	done
	for arch in amd64 arm64 386 arm mips mipsle; do
		dir="$DIST/prosvet-$VERSION-linux-$arch"
		cp scripts/install.sh "$dir/install.sh"
		cp README.md LICENSE "$dir/"
		tar -czf "$dir.tar.gz" -C "$DIST" "$(basename "$dir")"
		rm -rf "$dir"
	done
}

build_windows() {
	for arch in amd64 386; do
		out="$DIST/prosvet-$VERSION-windows-$arch"
		mkdir -p "$out"
		CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$out/prosvet.exe" ./cmd/prosvet
		cp scripts/windows/zapret.bat "$out/zapret.bat"
		cp LICENSE "$out/LICENSE.txt"
		cp docs/windows.txt "$out/ПРОЧТИ.txt"
		(cd "$DIST" && python3 -c "
import sys, zipfile, os
d = sys.argv[1]
with zipfile.ZipFile(d + '.zip', 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for f in sorted(os.listdir(d)):
        z.write(os.path.join(d, f), os.path.join(d, f))
" "$(basename "$out")")
		rm -rf "$out"
		echo "  windows/$arch  zapret.bat, prosvet.exe"
	done
}

case "$WHAT" in
	linux) build_linux ;;
	windows) build_windows ;;
	*) build_linux; build_windows ;;
esac

(cd "$DIST" && shopt -s nullglob && sha256sum ./*.tar.gz ./*.zip | sed 's#  \./#  #' >SHA256SUMS)
# Подпись: ключ из $PROSVET_SIGNING_KEY (CI) или из файла ключа мейнтейнера.
KEYFILE="${PROSVET_SIGNING_KEY_FILE:-$HOME/.config/prosvet-release/signing.key}"
if [ -n "${PROSVET_SIGNING_KEY:-}" ] || [ -f "$KEYFILE" ]; then
	PROSVET_SIGNING_KEY_FILE="$KEYFILE" go run ./tools/sign "$DIST/SHA256SUMS" >/dev/null
	echo "  SHA256SUMS подписан"
else
	echo "  (SHA256SUMS не подписан: нет ключа — установленные копии такой релиз не примут)"
fi
echo "  репозиторий обновлений: ${REPO:-нет}"
echo
ls -la "$DIST"
