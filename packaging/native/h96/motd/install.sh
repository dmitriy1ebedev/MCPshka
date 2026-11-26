#!/bin/bash
# Установка и откат безопасного MOTD входа H96 (MCP-173). Запуск от root:
#   install.sh apply     — резервная копия, установка, проверка
#   install.sh rollback  — вернуть файлы из последней копии
#   install.sh status    — что установлено сейчас
# sshd, PAM, сеть и службы не трогает: меняются только файлы сводки.
# ROOT=<каталог> переносит все пути под него (для тестов).
set -u
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ROOT=${ROOT:-}
SRC=$(cd "$(dirname "$0")" && pwd)
HOOK=$ROOT/etc/update-motd.d/32-router-dashboard
LIBEXEC=$ROOT/usr/local/libexec
DASH=$LIBEXEC/router-dashboard
REFRESH=$LIBEXEC/router-motd-refresh
RENDER=$LIBEXEC/router-motd-compact.awk
BIN=$ROOT/usr/local/bin
BACKUPS=$ROOT/var/backups/router-motd
SNAP=$ROOT/run/router-motd/snapshot
MARK='router-motd-login (MCP-173)'
die() { echo "ОШИБКА: $*" >&2; exit 1; }
installed() { grep -qF "$MARK" "$HOOK" 2>/dev/null; }
# put ИСТОЧНИК ЦЕЛЬ — атомарная замена; имя с точкой run-parts не запускает.
put() { install -m 755 "$1" "${2%/*}/.${2##*/}.new" && mv -f "${2%/*}/.${2##*/}.new" "$2"; }

apply() {
  [[ -f $HOOK ]] || die "нет $HOOK"
  local b f
  if installed; then
    # Повторная установка: копия оригинала уже есть, новую поверх не делаем.
    [[ -x $DASH ]] && b=$(readlink -f "$BACKUPS/mcp173-latest") && [[ -f $b/32-router-dashboard ]] || die "вход уже заменён, но копии оригинала нет"
  else
    b=$BACKUPS/mcp173-$(date +%Y%m%d-%H%M%S)
    mkdir -p "$b" || die "не создать $b"
    for f in "$HOOK" "$BIN/motd" "$BIN/motd-live"; do [[ -f $f ]] && { cp -p "$f" "$b/" || die "копия $f"; }; done
    ln -sfn "$b" "$BACKUPS/mcp173-latest"
  fi
  mkdir -p "$LIBEXEC" || die "не создать $LIBEXEC"
  # Полная цветная сводка уходит из входа в ручную команду `motd`.
  installed || put "$HOOK" "$DASH" || die "перенос сводки"
  put "$SRC/router-motd-refresh" "$REFRESH" || die "установка router-motd-refresh"
  # Рендерер шести строк: без него снимок — «data unavailable».
  put "$SRC/router-motd-compact.awk" "$RENDER" && chmod 644 "$RENDER" || die "установка router-motd-compact.awk"
  for f in "$BIN/motd" "$BIN/motd-live"; do
    [[ -f $f ]] && sed "s#/etc/update-motd.d/32-router-dashboard#/usr/local/libexec/router-dashboard#g" "$f" >"$b/.new" && put "$b/.new" "$f"
  done
  rm -f "$b/.new"
  # Первый снимок — тем же фоновым заданием systemd, что и при входе; не вышло — напрямую.
  if [[ -z $ROOT ]] && command -v systemd-run >/dev/null &&
    timeout 40 systemd-run --quiet --wait --collect --unit=router-motd-refresh -p Nice=19 -p RuntimeMaxSec=30 "$REFRESH" --force; then
    echo "снимок собран фоновым заданием systemd"
  else
    ROUTER_MOTD_DASHBOARD=$DASH ROUTER_MOTD_SNAPSHOT=$SNAP ROUTER_MOTD_RENDER=$RENDER bash "$REFRESH" --force
  fi
  put "$SRC/router-motd-login" "$HOOK" || die "установка входа"
  check || { echo "проверка не прошла — откат" >&2; rollback; exit 1; }
  echo "установлено; копия: $b; откат: $0 rollback"
}

# check — что увидит вход (текстовый снимок; цвет зависит от TERM): без управляющих байтов, не больше 4096 байт, быстро.
check() {
  local out; out=$(ROUTER_MOTD_SNAPSHOT=$SNAP ROUTER_MOTD_COLOR=never ROUTER_MOTD_RUNNER= ROUTER_MOTD_REFRESH=/bin/true timeout 5 sh "$HOOK") || return 1
  [[ -n $out ]] || { echo "снимок пуст" >&2; return 1; }
  local n; n=$(printf '%s' "$out" | wc -c)
  (( n <= 4096 )) || { echo "вывод больше 4096 байт" >&2; return 1; }
  [[ $(printf '%s' "$out" | LC_ALL=C tr -d '\000-\011\013-\037\177' | wc -c) == "$n" ]] || { echo "в выводе управляющие байты" >&2; return 1; }
  [[ -z $ROOT ]] && command -v sshd >/dev/null && { sshd -t || { echo "sshd -t не прошёл" >&2; return 1; }; }
  printf '%s\n' "$out"
}

rollback() {
  local b; b=$(readlink -f "$BACKUPS/mcp173-latest") && [[ -f $b/32-router-dashboard ]] || die "нет копии в $BACKUPS/mcp173-latest"
  put "$b/32-router-dashboard" "$HOOK" || die "возврат $HOOK"
  local f; for f in motd motd-live; do [[ -f $b/$f ]] && put "$b/$f" "$BIN/$f"; done
  rm -f "$DASH" "$REFRESH" "$RENDER" "$SNAP"
  echo "возвращено из $b"
}

status() {
  if installed; then echo "вход: безопасный снимок (MCP-173)"; else echo "вход: прежняя сводка"; fi
  [[ -e $BACKUPS/mcp173-latest ]] && echo "копия: $(readlink -f "$BACKUPS/mcp173-latest")"
  [[ -f $SNAP ]] && echo "снимок: $(stat -c '%s байт, %y' "$SNAP")"
  return 0
}

case ${1:-} in
  apply) apply ;;
  rollback) rollback ;;
  status) status ;;
  *) die "нужно: apply | rollback | status" ;;
esac
