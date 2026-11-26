#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG_DIR=/etc/mcpshka
CONFIG_SRC="$ROOT_DIR/packaging/tunnel/tunnel-client.yaml"
UNIT_SRC="$ROOT_DIR/packaging/tunnel/mcpshka-tunnel.service"
CONFIG_DST="$CONFIG_DIR/tunnel-client.yaml"
ENV_DST="$CONFIG_DIR/tunnel.env"
AUTH_DST="$CONFIG_DIR/mcp-auth-header"
MCPSHKA_ENV="$CONFIG_DIR/mcpshka.env"

if [[ $EUID -ne 0 ]]; then
  echo "install-tunnel.sh must run as root" >&2
  exit 1
fi
command -v tunnel-client >/dev/null

mkdir -p "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"
install -m 0600 "$CONFIG_SRC" "$CONFIG_DST"

if [[ -r "$AUTH_DST" ]]; then
  auth_header="$(head -1 "$AUTH_DST")"
  [[ "$auth_header" == "Bearer "* && -n "${auth_header#Bearer }" ]] || {
    echo "invalid existing mcp-auth-header" >&2
    exit 1
  }
else
  [[ -r "$MCPSHKA_ENV" ]]
  token="$(sed -n 's/^MCPSHKA_TOKEN=//p' "$MCPSHKA_ENV" | head -1)"
  [[ -n "$token" ]] || { echo "MCP bearer credential missing" >&2; exit 1; }
  printf 'Bearer %s\n' "$token" >"$AUTH_DST"
fi
chmod 600 "$AUTH_DST"

if [[ ! -f "$ENV_DST" ]]; then
  cat >"$ENV_DST" <<'EOF'
# Fill these two values from OpenAI Platform Tunnels / Runtime API keys.
CONTROL_PLANE_TUNNEL_ID=
CONTROL_PLANE_API_KEY=
EOF
fi
chmod 600 "$ENV_DST"

systemd-analyze verify "$UNIT_SRC" >/dev/null
install -m 0644 "$UNIT_SRC" /etc/systemd/system/mcpshka-tunnel.service
systemctl daemon-reload

echo "prepared: mcpshka-tunnel.service"
echo "config: $CONFIG_DST"
echo "credentials: $ENV_DST (0600)"
echo "service intentionally left disabled until real tunnel credentials are configured"
