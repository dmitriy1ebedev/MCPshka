#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BROKER_SOURCE="$ROOT_DIR/bin/mcpshka-broker"
TERMINAL_SOURCE="$ROOT_DIR/bin/mcpshka-container-terminal"
BROKER_TARGET="/usr/local/libexec/mcpshka-broker"
DIAG_TRAFFIC_SOURCE="$ROOT_DIR/packaging/native/diag-traffic-route-observe.py"
DIAG_TRAFFIC_TARGET="/usr/local/libexec/diag-traffic-route-observe"
DIAG_TRAFFIC_SHA256="6c60580612e0236e2b8f7afcfd6980771e62597606ed192fa57b54a450cde5e1"
TRAFFIC_RUNS_ROOT="/var/lib/diag/traffic-runs"
INSTALLER_TARGET="/usr/local/libexec/mcpshka-install-broker"
SELF_DEPLOY_SOURCE="$ROOT_DIR/packaging/native/release-gate.py"
SELF_DEPLOY_TARGET="/usr/local/libexec/mcpshka-self-deploy"
GUARDED_DEPLOY_SOURCE="$ROOT_DIR/packaging/native/self-deploy.sh"
GUARDED_DEPLOY_TARGET="/usr/local/libexec/mcpshka-self-deploy-guarded"
RESCUE_SOURCE="$ROOT_DIR/packaging/native/rescue.py"
RESCUE_TARGET="/usr/local/libexec/mcpshka-rescue.py"
MIGRATION_SOURCE="$ROOT_DIR/packaging/native/migrate-service-user.sh"
MIGRATION_TARGET="/usr/local/libexec/mcpshka-migrate-service-user"
AUTH_MIGRATOR_SOURCE="$ROOT_DIR/packaging/native/migrate-auth-registry.py"
AUTH_MIGRATOR_TARGET="/usr/local/libexec/mcpshka-migrate-auth-registry.py"
USER_UNIT_SOURCE="$ROOT_DIR/packaging/systemd/mcpshka.service"
USER_UNIT_TARGET="/usr/local/libexec/mcpshka-user.service"
CONFIG_DIR="/etc/mcpshka"
ENV_FILE="$CONFIG_DIR/mcpshka.env"
BROKER_CONFIG="$CONFIG_DIR/broker.json"
SUDOERS_FILE="/etc/sudoers.d/mcpshka-broker"
UNIT_SOURCE="$ROOT_DIR/packaging/systemd/mcpshka-broker.service"
UNIT_TARGET="/etc/systemd/system/mcpshka-broker.service"
MCPSHKA_USER="mcpshka"
BROKER_SOCKET="/run/mcpshka-broker/broker.sock"

if [[ $EUID -ne 0 ]]; then echo "install-broker.sh must run as root" >&2; exit 1; fi
# Only an independently reviewed root-owned snapshot may feed this installer.
python3 -I -S - "$BROKER_SOURCE" "$TERMINAL_SOURCE" "$UNIT_SOURCE" "$DIAG_TRAFFIC_SOURCE" \
  "$SELF_DEPLOY_SOURCE" "$GUARDED_DEPLOY_SOURCE" "$MIGRATION_SOURCE" "$AUTH_MIGRATOR_SOURCE" \
  "$USER_UNIT_SOURCE" "$ROOT_DIR/packaging/native/install-broker.sh" \
  "$ROOT_DIR/packaging/native/release-probe.py" \
  "$ROOT_DIR/packaging/native/state-store.py" \
  "$ROOT_DIR/packaging/native/codex-bridge.py" \
  "$RESCUE_SOURCE" <<'RELEASE_INPUTS'
import os, pathlib, stat, sys

def _verify_inputs(paths, owner_uid):
    for name in paths:
        path = pathlib.Path(name)
        if not path.is_absolute() or str(path) != os.path.abspath(name):
            raise ValueError("noncanonical release input: HOLD")
        for parent in path.parents:
            st = parent.lstat()
            sticky = st.st_uid == 0 and st.st_mode & stat.S_ISVTX
            if not stat.S_ISDIR(st.st_mode) or st.st_uid not in (0, owner_uid) or (st.st_mode & 0o022 and not sticky):
                raise ValueError("writable or aliased release parent: HOLD")
        st = path.lstat()
        if not stat.S_ISREG(st.st_mode) or st.st_uid != owner_uid or st.st_nlink != 1 or st.st_mode & 0o7022 or not 0 < st.st_size <= 128 << 20:
            raise ValueError("unsafe release input: HOLD")
        if path.suffix == ".py":
            if st.st_size > 1 << 20:
                raise ValueError("oversized release helper: HOLD")
            compile(path.read_bytes(), str(path), "exec")

_verify_inputs(sys.argv[1:], 0)
RELEASE_INPUTS
if [[ ! -x "$BROKER_SOURCE" ]]; then echo "missing broker binary: $BROKER_SOURCE" >&2; exit 1; fi
if [[ ! -f "$UNIT_SOURCE" || -L "$UNIT_SOURCE" ]]; then echo "missing/unsafe broker unit: $UNIT_SOURCE" >&2; exit 1; fi
if [[ ! -f "$DIAG_TRAFFIC_SOURCE" || -L "$DIAG_TRAFFIC_SOURCE" ]]; then echo "missing/unsafe DIAG traffic helper: $DIAG_TRAFFIC_SOURCE" >&2; exit 1; fi
actual_diag_sha256="$(sha256sum "$DIAG_TRAFFIC_SOURCE" | awk '{print $1}')"
[[ "$actual_diag_sha256" == "$DIAG_TRAFFIC_SHA256" ]] || { echo "DIAG traffic helper checksum mismatch: $actual_diag_sha256" >&2; exit 1; }
for helper in "$GUARDED_DEPLOY_SOURCE" "$MIGRATION_SOURCE" "$ROOT_DIR/packaging/native/install-broker.sh"; do
  [[ -f "$helper" && ! -L "$helper" && -x "$helper" ]] || { echo "missing/unsafe root helper: $helper" >&2; exit 1; }
  bash -n "$helper"
done
[[ -f "$AUTH_MIGRATOR_SOURCE" && ! -L "$AUTH_MIGRATOR_SOURCE" ]] || { echo "missing/unsafe auth migrator" >&2; exit 1; }
[[ -f "$USER_UNIT_SOURCE" && ! -L "$USER_UNIT_SOURCE" ]] || { echo "missing/unsafe user service unit" >&2; exit 1; }
systemd-analyze verify "$UNIT_SOURCE" >/dev/null

if ! getent group "$MCPSHKA_USER" >/dev/null; then groupadd --system "$MCPSHKA_USER"; fi
if ! id -u "$MCPSHKA_USER" >/dev/null 2>&1; then
  useradd --system --gid "$MCPSHKA_USER" --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin "$MCPSHKA_USER"
fi

install -d -o root -g root -m 0700 /var/lib/mcpshka-executor
install -d -o mcpshka -g mcpshka -m 2770 /var/lib/mcpshka/heavy-queue
install -d -o root -g root -m 0700 /var/lib/mcpshka-executor/candidates
install -d -o root -g root -m 0755 /usr/local/libexec
install -o root -g root -m 0755 "$BROKER_SOURCE" "$BROKER_TARGET"
install -o root -g root -m 0755 "$TERMINAL_SOURCE" /usr/local/libexec/mcpshka-container-terminal
install -o root -g root -m 0755 "$DIAG_TRAFFIC_SOURCE" "$DIAG_TRAFFIC_TARGET"
install -d -o root -g mcpshka -m 2770 "$TRAFFIC_RUNS_ROOT"
install -o root -g root -m 0755 "$ROOT_DIR/packaging/native/install-broker.sh" "$INSTALLER_TARGET"
install -o root -g root -m 0755 "$SELF_DEPLOY_SOURCE" "$SELF_DEPLOY_TARGET"
install -o root -g root -m 0755 "$GUARDED_DEPLOY_SOURCE" "$GUARDED_DEPLOY_TARGET"
install -o root -g root -m 0755 "$ROOT_DIR/packaging/native/release-probe.py" /usr/local/libexec/mcpshka-release-probe.py
install -o root -g root -m 0755 "$ROOT_DIR/packaging/native/state-store.py" /usr/local/libexec/mcpshka-state-store.py
install -o root -g root -m 0755 "$ROOT_DIR/packaging/native/codex-bridge.py" /usr/local/libexec/mcpshka-codex-bridge.py
install -o root -g root -m 0755 "$ROOT_DIR/packaging/cloud/dispatch.py" /usr/local/libexec/mcpshka-cloud-agent.py
install -o root -g root -m 0755 "$ROOT_DIR/packaging/gitverse/snapshot_immutable.py" /usr/local/libexec/mcpshka-cloud-snapshot.py
install -o root -g root -m 0755 "$RESCUE_SOURCE" "$RESCUE_TARGET"
install -o root -g root -m 0755 "$MIGRATION_SOURCE" "$MIGRATION_TARGET"
install -o root -g root -m 0755 "$AUTH_MIGRATOR_SOURCE" "$AUTH_MIGRATOR_TARGET"
install -o root -g root -m 0644 "$USER_UNIT_SOURCE" "$USER_UNIT_TARGET"
# Preserve service-user auth traversal when refreshing the broker.
if [[ -e "$CONFIG_DIR" || -L "$CONFIG_DIR" ]]; then
  [[ -d "$CONFIG_DIR" && ! -L "$CONFIG_DIR" ]] || { echo "unsafe config directory" >&2; exit 1; }
else
  install -d -o root -g root -m 0700 "$CONFIG_DIR"
fi

config_tmp="$(mktemp "$CONFIG_DIR/.broker.json.XXXXXX")"
unit_tmp="$(mktemp /etc/systemd/system/.mcpshka-broker.XXXXXX)"
cleanup() { rm -f "$config_tmp" "$unit_tmp"; }
trap cleanup EXIT

python3 - "$ENV_FILE" "$config_tmp" <<'PY'
import json, pathlib, sys
env_path = pathlib.Path(sys.argv[1])
out_path = pathlib.Path(sys.argv[2])
wanted = {
    "MCPSHKA_SYSTEMD_SERVICES": [],
    "MCPSHKA_SYSTEMD_RESTART_SERVICES": [],
    "MCPSHKA_SYSTEMD_MUTATION_SERVICES": [],
    "MCPSHKA_DOCKER_READ_CONTAINERS": [],
    "MCPSHKA_DOCKER_RESTART_CONTAINERS": [],
    "MCPSHKA_DOCKER_MUTATION_CONTAINERS": [],
    "MCPSHKA_DOCKER_TERMINAL_CONTAINERS": [],
}
if env_path.exists():
    for raw in env_path.read_text(errors='replace').splitlines():
        if '=' not in raw or raw.lstrip().startswith('#'):
            continue
        key, value = raw.split('=', 1)
        if key in wanted:
            wanted[key] = [x.strip() for x in value.split(',') if x.strip()]
status = ["mcpshka.service", "mcpshka-tunnel.service", "mcpshka-broker.service"]
for name in wanted["MCPSHKA_SYSTEMD_SERVICES"] + wanted["MCPSHKA_SYSTEMD_RESTART_SERVICES"] + wanted["MCPSHKA_SYSTEMD_MUTATION_SERVICES"]:
    if name not in status:
        status.append(name)
payload = {
    "systemd_status": status,
    "systemd_restart": wanted["MCPSHKA_SYSTEMD_RESTART_SERVICES"],
    "systemd_mutation": wanted["MCPSHKA_SYSTEMD_MUTATION_SERVICES"],
    "docker_read": wanted["MCPSHKA_DOCKER_READ_CONTAINERS"],
    "docker_restart": wanted["MCPSHKA_DOCKER_RESTART_CONTAINERS"],
    "docker_mutation": wanted["MCPSHKA_DOCKER_MUTATION_CONTAINERS"],
    "docker_terminal": wanted["MCPSHKA_DOCKER_TERMINAL_CONTAINERS"],
}
out_path.write_text(json.dumps(payload, sort_keys=True) + '\n', encoding='utf-8')
PY
chown root:root "$config_tmp"
chmod 0600 "$config_tmp"
mv -f "$config_tmp" "$BROKER_CONFIG"

install -o root -g root -m 0644 "$UNIT_SOURCE" "$unit_tmp"
mv -f "$unit_tmp" "$UNIT_TARGET"
systemctl daemon-reload
"$BROKER_TARGET" home-priority --startup
systemctl enable mcpshka-broker.service >/dev/null
systemctl restart mcpshka-broker.service
systemctl --quiet is-active mcpshka-broker.service

broker_ready=0
# Service-UID acceptance uses the live controller; other PIDs are denied.
for _ in {1..25}; do
  if [[ -S "$BROKER_SOCKET" ]] &&
     "$BROKER_TARGET" call systemctl show --no-pager --property=Id,ActiveState mcpshka.service >/dev/null 2>&1; then
    broker_ready=1
    break
  fi
  sleep 0.2
done
if [[ "$broker_ready" != 1 ]]; then
  echo "broker socket transport did not become usable" >&2
  exit 1
fi

# Legacy bootstrap transport is removed only after the Unix-socket broker is proven usable.
rm -f "$SUDOERS_FILE"

echo "socket broker installed; verify controller RPC before deployment"
