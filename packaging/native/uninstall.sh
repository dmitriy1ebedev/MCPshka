#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "uninstall.sh must run as root" >&2
  exit 1
fi

systemctl disable --now mcpshka.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/mcpshka.service
systemctl daemon-reload
rm -f /usr/local/bin/mcpshka

echo "MCPshka service and current binary removed."
echo "State/config and /usr/local/bin/mcpshka.previous were preserved."
