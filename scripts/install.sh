#!/bin/sh
# Установка Просвета из распакованного архива: служба, автозапуск, подбор.
#   ./install.sh            — установить и подобрать стратегии
#   ./install.sh --no-tune  — только установить
set -e
cd "$(dirname "$0")"
if [ "$(id -u)" != 0 ]; then
	if command -v sudo >/dev/null 2>&1; then exec sudo "$0" "$@"; fi
	if command -v doas >/dev/null 2>&1; then exec doas "$0" "$@"; fi
	echo "Нужны права root: запустите через sudo" >&2
	exit 1
fi
exec ./prosvet install "$@"
