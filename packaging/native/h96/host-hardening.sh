#!/bin/bash
# H96: защита от многочасового своп-трэша и потери журнала (инцидент 09.11.2026,
# docs/history/INCIDENT_2026-11-09_H96_MEMORY_HANG.md). Идемпотентно, запускать от root.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)

# 1. Журнал на диске: Armbian переводит journald в RAM (relinquish-var) из двух мест —
#    truncate-logs (каждые 15 мин) и ramlog write (раз в сутки, cron.daily). После
#    ramlog journald пишет в /run до ребута: 15.11 при отключении света пропали
#    00:57–14:57 (truncate-logs не возвращает журнал на диск, пока /run непуст).
for S in /usr/lib/armbian/armbian-truncate-logs /usr/lib/armbian/armbian-ramlog; do
  if [ -f "$S" ] && ! dpkg-divert --list "$S" | grep -q .; then
    dpkg-divert --local --rename --divert "$S.armbian" --add "$S"
  fi
  if [ -f "$S.armbian" ]; then
    sed 's|^\(\s*\)journalctl --relinquish-var.*|\1: # H96: relinquish-var отключён — журнал на диске|' "$S.armbian" > "$S"
    chmod 755 "$S"; bash -n "$S"
    ! grep -q '^\s*journalctl --relinquish-var' "$S"
  fi
done
journalctl --flush

# 2. earlyoom вместо зависания: ядро без PSI, OOM-killer молчит, пока жив своп.
command -v earlyoom >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y -q earlyoom
install -m 644 "$here/earlyoom.default" /etc/default/earlyoom
systemctl enable --now earlyoom; systemctl restart earlyoom

# 3. Без свопа на eMMC: только zram, иначе часы трэша вместо быстрого OOM.
if swapon --show=NAME --noheadings | grep -qx /swapfile; then swapoff /swapfile; fi
sed -i 's|^/swapfile none swap|#H96 09.11 своп на eMMC отключён: /swapfile none swap|' /etc/fstab

systemctl is-active earlyoom; swapon --show; journalctl --header | grep -m1 'File path'
