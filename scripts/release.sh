#!/usr/bin/env bash
# Выпуск новой версии: scripts/release.sh 1.2.3
# Меняет версию в коде, коммитит, ставит тег v1.2.3 и отправляет на GitHub.
# Дальше GitHub Actions соберёт все платформы, подпишет SHA256SUMS ключом
# из секрета PROSVET_SIGNING_KEY и опубликует релиз — установленные копии
# подхватят его в течение шести часов (или сразу: prosvet update).
set -euo pipefail
cd "$(dirname "$0")/.."
V="${1:?укажите версию, например 1.0.1}"
V="${V#v}"
[[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9a-z.]+)?$ ]] || { echo "версия вида 1.2.3" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "есть незакоммиченные изменения" >&2; exit 1; }
git rev-parse "v$V" >/dev/null 2>&1 && { echo "тег v$V уже есть" >&2; exit 1; }
sed -i "s/^var Version = \".*\"/var Version = \"$V\"/" internal/core/core.go
git commit -qam "Release v$V"
git tag -a "v$V" -m "v$V"
git push -q origin HEAD
git push -q origin "v$V"
echo "v$V отправлен — сборка и публикация идут в GitHub Actions"
