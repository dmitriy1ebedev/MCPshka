#!/usr/bin/env bash
set -euo pipefail
umask 077

BACKUP_ROOT=/var/backups/mcpshka
if [[ $# -gt 0 ]]; then
  BACKUP="$1"
else
  [[ -r "$BACKUP_ROOT/latest" ]] || { echo "no migration backup pointer" >&2; exit 1; }
  BACKUP="$(cat "$BACKUP_ROOT/latest")"
fi
[[ -d "$BACKUP" ]] || { echo "backup not found: $BACKUP" >&2; exit 1; }
[[ $EUID -eq 0 ]] || { echo "must run as root" >&2; exit 1; }
exec 9>/run/mcpshka-self-deploy.lock
flock -n 9 || { echo "deployment busy: HOLD" >&2; exit 75; }

systemctl stop mcpshka-tunnel.service >/dev/null 2>&1 || true
systemctl stop mcpshka.service >/dev/null 2>&1 || true

[[ -d /root/repos/mcpshka && ! -e /root/repos/remoutka ]] && mv /root/repos/mcpshka /root/repos/remoutka
[[ -d /var/lib/mcpshka && ! -e /var/lib/remoutka ]] && mv /var/lib/mcpshka /var/lib/remoutka
[[ -d /var/lib/mcpshka-tunnel && ! -e /var/lib/remoutka-tunnel ]] && mv /var/lib/mcpshka-tunnel /var/lib/remoutka-tunnel
if [[ -d /opt/mcpshka && ! -e /opt/remoutka ]]; then
  target="$(readlink -f /usr/local/bin/tunnel-client 2>/dev/null || true)"
  mv /opt/mcpshka /opt/remoutka
  if [[ "$target" == /opt/mcpshka/* ]]; then
    old_target="$(printf '%s' "$target" | sed 's#^/opt/mcpshka/#/opt/remoutka/#')"
    ln -sfn "$old_target" /usr/local/bin/tunnel-client
  fi
fi

rm -rf /etc/remoutka
cp -a "$BACKUP/etc-remoutka" /etc/remoutka
install -m 0644 "$BACKUP/remoutka.service" /etc/systemd/system/remoutka.service
[[ -f "$BACKUP/remoutka-tunnel.service" ]] && install -m 0644 "$BACKUP/remoutka-tunnel.service" /etc/systemd/system/remoutka-tunnel.service
install -m 0755 "$BACKUP/remoutka" /usr/local/bin/remoutka
[[ -f "$BACKUP/remoutka.previous" ]] && install -m 0755 "$BACKUP/remoutka.previous" /usr/local/bin/remoutka.previous

systemctl daemon-reload
systemctl enable remoutka.service >/dev/null
systemctl start remoutka.service
systemctl --quiet is-active remoutka.service
if [[ -f /etc/systemd/system/remoutka-tunnel.service && -f /etc/remoutka/tunnel.env ]]; then
  systemctl enable remoutka-tunnel.service >/dev/null
  systemctl start remoutka-tunnel.service
fi

systemctl disable mcpshka-tunnel.service >/dev/null 2>&1 || true
systemctl disable mcpshka.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/mcpshka.service /etc/systemd/system/mcpshka-tunnel.service
rm -f /usr/local/bin/mcpshka /usr/local/bin/mcpshka.previous
rm -rf /etc/mcpshka
systemctl daemon-reload

echo "rolled back migration using: $BACKUP"
