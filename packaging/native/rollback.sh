#!/usr/bin/env bash
set -euo pipefail
umask 077

TARGET="/usr/local/bin/mcpshka"
PREVIOUS="/usr/local/bin/mcpshka.previous"

if [[ $EUID -ne 0 ]]; then
  echo "rollback.sh must run as root" >&2
  exit 1
fi
exec 9>/run/mcpshka-self-deploy.lock
flock -n 9 || { echo "deployment busy: HOLD" >&2; exit 75; }
if [[ ! -x "$PREVIOUS" ]]; then
  echo "no rollback binary: $PREVIOUS" >&2
  exit 1
fi

python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$PREVIOUS" version >/dev/null
current_tmp=""
if [[ -x "$TARGET" ]]; then
  python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$TARGET" version >/dev/null
  current_tmp="$(mktemp /usr/local/bin/.mcpshka.rollback.XXXXXX)"
  cp -p "$TARGET" "$current_tmp"
  chmod 755 "$current_tmp"
fi

restore_tmp="$(mktemp /usr/local/bin/.mcpshka.restore.XXXXXX)"
cp -p "$PREVIOUS" "$restore_tmp"
chmod 755 "$restore_tmp"
python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$restore_tmp" version >/dev/null
mv -f "$restore_tmp" "$TARGET"

if ! systemctl restart mcpshka.service || ! systemctl --quiet is-active mcpshka.service; then
  if [[ -n "$current_tmp" && -x "$current_tmp" ]]; then
    mv -f "$current_tmp" "$TARGET"
    systemctl restart mcpshka.service || true
  fi
  echo "rollback target failed health/start check; restored former current binary" >&2
  exit 1
fi

if [[ -n "$current_tmp" && -x "$current_tmp" ]]; then
  mv -f "$current_tmp" "$PREVIOUS"
fi
printf 'rolled back to: %s\n' "$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$TARGET" version)"
