#!/usr/bin/env bash
set -euo pipefail
umask 077

MODE="${1:-}"
ID="${2:-}"
ROOT=/root/repos/mcpshka
SERVICE=mcpshka.service
TUNNEL=mcpshka-tunnel.service
BROKER=mcpshka-broker.service
UNIT_SOURCE=/usr/local/libexec/mcpshka-user.service
UNIT_TARGET=/etc/systemd/system/mcpshka.service
CONFIG_DIR=/etc/mcpshka
ENV_FILE="$CONFIG_DIR/mcpshka.env"
AUTH_FILE="$CONFIG_DIR/auth.json"
RAW_HEADER="$CONFIG_DIR/mcp-auth-header"
STATUS_ROOT=/var/lib/mcpshka/migrations
STATUS_DIR="$STATUS_ROOT/$ID"
STATUS="$STATUS_DIR/status.json"
PRIVATE_ROOT=/var/lib/mcpshka-broker/migrations
PRIVATE_DIR="$PRIVATE_ROOT/$ID"
BACKUP_UNIT="$PRIVATE_DIR/mcpshka.service.before"
META="$PRIVATE_DIR/meta"
LOCK=/run/mcpshka-service-user-migrate.lock
PROJECT_ROOTS=(
  /root/repos/mcpshka
  /root/repos/lane
  /root/repos/diag
)
CHATOPS_ROOT=/var/lib/chatops

[[ "$MODE" == "apply" || "$MODE" == "rollback" ]] || { echo "invalid mode" >&2; exit 2; }
case "$ID" in ""|*[!0-9a-f-]*) echo "invalid migration id" >&2; exit 2 ;; esac
[[ $EUID -eq 0 ]] || { echo "migration must run as root" >&2; exit 1; }

mkdir -p "$STATUS_DIR" "$PRIVATE_DIR"
chown root:mcpshka "$STATUS_ROOT" "$STATUS_DIR"
chmod 0750 "$STATUS_ROOT" "$STATUS_DIR"
chown root:root "$PRIVATE_ROOT" "$PRIVATE_DIR"
chmod 700 "$PRIVATE_ROOT" "$PRIVATE_DIR"
exec 9>"$LOCK"
flock -n 9 || { echo "another service-user migration is active" >&2; exit 75; }

write_status() {
  local state="$1" detail="${2:-}"
  local tmp
  tmp="$(mktemp "$STATUS_DIR/.status.XXXXXX")"
  python3 - "$tmp" "$ID" "$state" "$detail" <<'PY'
import json, pathlib, sys, datetime
path, mid, state, detail = sys.argv[1:]
obj={"migration_id":mid,"state":state,"detail":detail,"updated_at":datetime.datetime.now(datetime.timezone.utc).isoformat()}
pathlib.Path(path).write_text(json.dumps(obj,sort_keys=True)+"\n",encoding="utf-8")
PY
  chown root:mcpshka "$tmp"
  chmod 0640 "$tmp"
  mv -f "$tmp" "$STATUS"
}

health_ok() {
  local i health ready user
  for i in $(seq 1 20); do
    health="$(curl -fsS --max-time 2 http://127.0.0.1:8765/healthz 2>/dev/null || true)"
    ready="$(curl -fsS --max-time 2 http://127.0.0.1:8768/readyz 2>/dev/null || true)"
    user="$(systemctl show --no-pager --property=User --value "$SERVICE" 2>/dev/null || true)"
    if systemctl -q is-active "$SERVICE" &&
       systemctl -q is-active "$TUNNEL" &&
       [[ "$health" == *'"status":"ok"'* ]] &&
       [[ "$ready" == "ready" ]] &&
       [[ "$user" == "mcpshka" ]] &&
       getent ahostsv4 api.openai.com >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

root_health_ok() {
  local i health ready user
  for i in $(seq 1 15); do
    health="$(curl -fsS --max-time 2 http://127.0.0.1:8765/healthz 2>/dev/null || true)"
    ready="$(curl -fsS --max-time 2 http://127.0.0.1:8768/readyz 2>/dev/null || true)"
    user="$(systemctl show --no-pager --property=User --value "$SERVICE" 2>/dev/null || true)"
    if systemctl -q is-active "$SERVICE" &&
       systemctl -q is-active "$TUNNEL" &&
       [[ "$health" == *'"status":"ok"'* ]] &&
       [[ "$ready" == "ready" ]] &&
       [[ -z "$user" || "$user" == "root" ]]; then
      return 0
    fi
    sleep 2
  done
  return 1
}

restore_root_mode() {
  if [[ -f "$BACKUP_UNIT" ]]; then
    install -o root -g root -m 0644 "$BACKUP_UNIT" "$UNIT_TARGET"
  fi
  if [[ -f "$META" ]]; then
    read -r root_gid root_mode etc_gid etc_mode auth_gid auth_mode <"$META"
    chgrp "$root_gid" /root || true
    chmod "$root_mode" /root || true
    chgrp "$etc_gid" "$CONFIG_DIR" || true
    chmod "$etc_mode" "$CONFIG_DIR" || true
    if [[ -f "$AUTH_FILE" ]]; then
      chgrp "$auth_gid" "$AUTH_FILE" || true
      chmod "$auth_mode" "$AUTH_FILE" || true
    fi
  fi
  for project in "${PROJECT_ROOTS[@]}"; do
    [[ -d "$project" && ! -L "$project" ]] || continue
    chown -R root:root "$project" || true
  done
  chown -R root:root /var/lib/mcpshka || true
  systemctl daemon-reload || true
  systemctl restart "$SERVICE" || true
}

if [[ "$MODE" == "rollback" ]]; then
  [[ -f "$BACKUP_UNIT" && -f "$META" ]] || { echo "migration backup missing" >&2; exit 1; }
  write_status rolling_back
  restore_root_mode
  root_health_ok || { write_status rollback_failed "root service did not recover"; exit 1; }
  write_status rolled_back
  exit 0
fi

current_user="$(systemctl show --no-pager --property=User --value "$SERVICE" 2>/dev/null || true)"
if [[ "$current_user" == "mcpshka" ]]; then
  if health_ok; then
    write_status already_active "service already runs as mcpshka"
    exit 0
  fi
  write_status failed "service claims mcpshka user but health verification failed"
  exit 1
fi

write_status preflight
[[ -f "$UNIT_SOURCE" && ! -L "$UNIT_SOURCE" ]] || { write_status failed "unit source invalid"; exit 1; }
systemd-analyze verify "$UNIT_SOURCE" >/dev/null || { write_status failed "unit verify failed"; exit 1; }
systemctl -q is-active "$BROKER" || { write_status failed "broker inactive"; exit 1; }
id -u mcpshka >/dev/null 2>&1 || { write_status failed "mcpshka user missing"; exit 1; }
grep -q '^MCPSHKA_AUTH_FILE=' "$ENV_FILE" || { write_status failed "auth registry not configured"; exit 1; }
[[ -f "$AUTH_FILE" && ! -L "$AUTH_FILE" ]] || { write_status failed "auth registry invalid"; exit 1; }

# The tunnel uses /etc/mcpshka/tunnel.env, so the legacy bearer is not needed
# by the main daemon once the digest registry is configured. Remove it atomically
# before switching users; the running root daemon is unaffected.
python3 - "$ENV_FILE" <<'PY'
import os, pathlib, sys, tempfile
path = pathlib.Path(sys.argv[1])
lines = path.read_text(errors="strict").splitlines()
if not any(line.startswith("MCPSHKA_AUTH_FILE=") for line in lines):
    raise SystemExit("auth registry setting disappeared")
filtered = [line for line in lines if not line.startswith("MCPSHKA_TOKEN=")]
fd, tmp = tempfile.mkstemp(prefix=".mcpshka.env.", dir=str(path.parent))
try:
    os.fchmod(fd, 0o600)
    os.fchown(fd, 0, 0)
    data = ("\n".join(filtered) + "\n").encode()
    os.write(fd, data)
    os.fsync(fd)
finally:
    os.close(fd)
os.replace(tmp, path)
PY
if grep -q '^MCPSHKA_TOKEN=' "$ENV_FILE"; then
  write_status failed "legacy plaintext token removal failed"
  exit 1
fi
[[ -f "$RAW_HEADER" ]] || { write_status failed "tunnel auth header missing"; exit 1; }
[[ "$(stat -c %a "$RAW_HEADER")" == "600" ]] || { write_status failed "tunnel auth header permissions unsafe"; exit 1; }

cp -p "$UNIT_TARGET" "$BACKUP_UNIT"
printf '%s %s %s %s %s %s\n'   "$(stat -c %g /root)" "$(stat -c %a /root)"   "$(stat -c %g "$CONFIG_DIR")" "$(stat -c %a "$CONFIG_DIR")"   "$(stat -c %g "$AUTH_FILE")" "$(stat -c %a "$AUTH_FILE")" >"$META"
chmod 600 "$BACKUP_UNIT" "$META"

write_status preparing_permissions
chgrp mcpshka /root
chmod 0710 /root

for project in "${PROJECT_ROOTS[@]}"; do
  [[ -d "$project" && ! -L "$project" ]] || continue
  chown -R mcpshka:mcpshka "$project"
  chmod -R u+rwX,g+rwX "$project"
  find "$project" -type d -exec chmod g+s {} +
done

if [[ -d "$CHATOPS_ROOT" && ! -L "$CHATOPS_ROOT" ]]; then
  chgrp -R mcpshka "$CHATOPS_ROOT"
  chmod -R g+rwX "$CHATOPS_ROOT"
  find "$CHATOPS_ROOT" -type d -exec chmod g+s {} +
fi

chown -R mcpshka:mcpshka /var/lib/mcpshka
chgrp mcpshka "$CONFIG_DIR"
chmod 0710 "$CONFIG_DIR"
chgrp mcpshka "$AUTH_FILE"
chmod 0640 "$AUTH_FILE"
# Raw bearer remains root-only even though the service can traverse CONFIG_DIR.
chown root:root "$RAW_HEADER"
chmod 0600 "$RAW_HEADER"

install -o root -g root -m 0644 "$UNIT_SOURCE" "$UNIT_TARGET"
systemctl daemon-reload
write_status restarting
if ! systemctl restart "$SERVICE"; then
  write_status recovering "restart failed"
  restore_root_mode
  root_health_ok && write_status rolled_back "restart failed" || write_status rollback_failed "restart failed"
  exit 1
fi
if ! health_ok; then
  write_status recovering "non-root health verification failed"
  restore_root_mode
  root_health_ok && write_status rolled_back "health verification failed" || write_status rollback_failed "health verification failed"
  exit 1
fi
write_status succeeded
echo "mcpshka.service migrated to User=mcpshka"
