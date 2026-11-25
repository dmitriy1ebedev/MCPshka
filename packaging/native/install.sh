#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SOURCE_BIN="$ROOT_DIR/bin/mcpshka"
TARGET_BIN="/usr/local/bin/mcpshka"
PREVIOUS_BIN="/usr/local/bin/mcpshka.previous"
UNIT_SOURCE="$ROOT_DIR/packaging/systemd/mcpshka.service"
UNIT_TARGET="/etc/systemd/system/mcpshka.service"
CONFIG_DIR="/etc/mcpshka"
ENV_FILE="$CONFIG_DIR/mcpshka.env"
AUTH_FILE="$CONFIG_DIR/auth.json"
AUTH_HEADER_FILE="$CONFIG_DIR/mcp-auth-header"

if [[ $EUID -ne 0 ]]; then
  echo "install.sh must run as root" >&2
  exit 1
fi
exec 9>/run/mcpshka-self-deploy.lock
flock -n 9 || { echo "deployment busy: HOLD" >&2; exit 75; }
if [[ ! -x "$SOURCE_BIN" ]]; then
  echo "missing built binary: $SOURCE_BIN" >&2
  exit 1
fi

new_version="$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$SOURCE_BIN" version)"
if [[ "$new_version" != mcpshka* ]]; then
  echo "source binary failed version check" >&2
  exit 1
fi

mkdir -p "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"
if [[ ! -f "$ENV_FILE" ]]; then
  token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  token_hash="$(printf '%s' "$token" | sha256sum | awk '{print $1}')"
  {
    printf 'MCPSHKA_HTTP_ADDR=127.0.0.1:8765\n'
    printf 'MCPSHKA_AUTH_FILE=%s\n' "$AUTH_FILE"
    printf 'MCPSHKA_STATE_DIR=/var/lib/mcpshka/jobs\n'
    printf 'MCPSHKA_AUDIT_PATH=/var/lib/mcpshka/audit.jsonl\n'
  } >"$ENV_FILE"
  printf 'Bearer %s\n' "$token" >"$AUTH_HEADER_FILE"
  printf '{"version":1,"credentials":[{"id":"openai-tunnel","token_sha256":"%s"}]}\n' "$token_hash" >"$AUTH_FILE"
fi
chmod 600 "$ENV_FILE"
[[ ! -e "$AUTH_FILE" ]] || chmod 600 "$AUTH_FILE"
[[ ! -e "$AUTH_HEADER_FILE" ]] || chmod 600 "$AUTH_HEADER_FILE"

if [[ -x "$TARGET_BIN" ]]; then
  python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$TARGET_BIN" version >/dev/null
  prev_tmp="$(mktemp /usr/local/bin/.mcpshka.previous.XXXXXX)"
  cp -p "$TARGET_BIN" "$prev_tmp"
  chmod 755 "$prev_tmp"
  mv -f "$prev_tmp" "$PREVIOUS_BIN"
fi

new_tmp="$(mktemp /usr/local/bin/.mcpshka.new.XXXXXX)"
install -m 0755 "$SOURCE_BIN" "$new_tmp"
python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$new_tmp" version >/dev/null
mv -f "$new_tmp" "$TARGET_BIN"

systemd-analyze verify "$UNIT_SOURCE" >/dev/null
unit_tmp="$(mktemp /etc/systemd/system/.mcpshka-unit.XXXXXX)"
install -m 0644 "$UNIT_SOURCE" "$unit_tmp"
mv -f "$unit_tmp" "$UNIT_TARGET"

systemctl daemon-reload
systemctl enable mcpshka.service >/dev/null
systemctl restart mcpshka.service
systemctl --quiet is-active mcpshka.service

installed_version="$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$TARGET_BIN" version)"
printf 'installed: %s\n' "$installed_version"
printf 'listen: 127.0.0.1:8765\n'
printf 'auth registry: %s (0600)\n' "$AUTH_FILE"
printf 'tunnel auth header: %s (0600)\n' "$AUTH_HEADER_FILE"
