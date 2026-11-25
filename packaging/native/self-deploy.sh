#!/usr/bin/env bash
set -euo pipefail
umask 077

MODE="${1:-}"
ID="${2:-}"
CANDIDATE="${3:-}"
EXPECTED_NEW="${4:-}"
EXPECTED_OLD="${5:-}"
STATUS="${6:-}"
EXPECTED_NEW_SHA="${7:-}"
EXPECTED_OLD_SHA="${8:-}"
BROKER_CANDIDATE="${9:-}"
BROKER_NEW_SHA="${10:-}"
BROKER_OLD_SHA="${11:-}"
BROKER_TARGET=/usr/local/libexec/mcpshka-broker
BROKER_PREVIOUS=/usr/local/libexec/mcpshka-broker.previous
BROKER_SERVICE=mcpshka-broker.service
PAIRED=0
if [[ -n "$BROKER_CANDIDATE" || -n "$BROKER_NEW_SHA" || -n "$BROKER_OLD_SHA" ]]; then
  PAIRED=1
  [[ "$BROKER_CANDIDATE" =~ ^/var/lib/mcpshka-release-gates/[a-f0-9]{64}/broker-candidate$ ]] || exit 75
  [[ "$BROKER_NEW_SHA" =~ ^[a-f0-9]{64}$ && "$BROKER_OLD_SHA" =~ ^[a-f0-9]{64}$ ]] || exit 75
fi

TARGET=/usr/local/bin/mcpshka
PREVIOUS=/usr/local/bin/mcpshka.previous
SERVICE=mcpshka.service
TUNNEL=mcpshka-tunnel.service
LOCK=/run/mcpshka-self-deploy.lock
STATE_HELPER=/usr/local/libexec/mcpshka-state-store.py
AUTH_MIGRATOR=/usr/local/libexec/mcpshka-migrate-auth-registry.py
STARTED_AT="$(date -u +%FT%TZ)"
EXTERNAL_DNS=unknown
EXTERNAL_HTTPS=unknown
CLEANUP_JSON='{}'

[[ "$MODE" == "deploy" ]] || { echo "invalid mode" >&2; exit 2; }
case "$ID" in
  ""|*[!0-9a-f-]*) echo "invalid deployment id" >&2; exit 2 ;;
esac
[[ "$(dirname -- "$CANDIDATE")" == "/root/repos/mcpshka/bin" ]] || { echo "invalid candidate directory" >&2; exit 2; }
case "$(basename -- "$CANDIDATE")" in
  mcpshka-*.candidate) ;;
  *) echo "invalid candidate name" >&2; exit 2 ;;
esac
[[ "$(dirname -- "$STATUS")" == "/var/lib/mcpshka/deploys" ]] || { echo "invalid status path" >&2; exit 2; }

write_status() {
  local state="$1"
  local installed="${2:-}"
  local rollback="${3:-}"
  local finished="${4:-}"
  local tmp
  if [[ -f /var/lib/mcpshka/state.mode ]]; then
    printf '{"deployment_id":"%s","state":"%s","target_version":"%s","current_version":"%s","installed_version":"%s","rollback_version":"%s","started_at":"%s","finished_at":"%s","external_dns":"%s","external_https":"%s","cleanup":%s}\n' "$ID" "$state" "$EXPECTED_NEW" "$EXPECTED_OLD" "$installed" "$rollback" "$STARTED_AT" "$finished" "$EXTERNAL_DNS" "$EXTERNAL_HTTPS" "$CLEANUP_JSON" | python3 "$STATE_HELPER" write /var/lib/mcpshka "deploys/$ID.json" >/dev/null
    return
  fi
  mkdir -p "$(dirname -- "$STATUS")"
  chmod 700 "$(dirname -- "$STATUS")"
  tmp="$(mktemp "${STATUS}.tmp.XXXXXX")"
  printf '{"deployment_id":"%s","state":"%s","target_version":"%s","current_version":"%s","installed_version":"%s","rollback_version":"%s","started_at":"%s","finished_at":"%s","external_dns":"%s","external_https":"%s","cleanup":%s}\n' \
    "$ID" "$state" "$EXPECTED_NEW" "$EXPECTED_OLD" "$installed" "$rollback" "$STARTED_AT" "$finished" "$EXTERNAL_DNS" "$EXTERNAL_HTTPS" "$CLEANUP_JSON" >"$tmp"
  if getent group mcpshka >/dev/null 2>&1; then
    chown root:mcpshka "$tmp"
    chmod 0640 "$tmp"
  else
    chown root:root "$tmp"
    chmod 0600 "$tmp"
  fi
  mv -f "$tmp" "$STATUS"
}

verify_hash_pin() {
  [[ "$2" =~ ^[a-f0-9]{64}$ ]] || return 1
  local observed
  observed="$(sha256sum -- "$1")" || return 1
  [[ "${observed%% *}" == "$2" ]]
}

version_of() {
  local out
  out="$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$1" version)" || return 1
  [[ "$out" == mcpshka\ * ]] || return 1
  printf '%s' "${out#mcpshka }"
}

stage_candidate_nofollow() {
  local src="$1" dst="$2"
  python3 - "$src" "$dst" <<'PY'
import os, stat, sys
src, dst = sys.argv[1:]
flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
fd = os.open(src, flags)
try:
    st = os.fstat(fd)
    if not stat.S_ISREG(st.st_mode):
        raise SystemExit("candidate is not a regular file")
    out = os.open(dst, os.O_WRONLY | os.O_TRUNC)
    try:
        while True:
            chunk = os.read(fd, 1024 * 1024)
            if not chunk:
                break
            view = memoryview(chunk)
            while view:
                n = os.write(out, view)
                view = view[n:]
        os.fchmod(out, 0o755)
        os.fsync(out)
    finally:
        os.close(out)
finally:
    os.close(fd)
PY
}

observe_external_connectivity() {
  EXTERNAL_DNS=unavailable
  EXTERNAL_HTTPS=unavailable
  if timeout 4 getent ahostsv4 api.openai.com >/dev/null 2>&1; then EXTERNAL_DNS=available; fi
  if curl -sS -o /dev/null --connect-timeout 4 --max-time 8 https://api.openai.com/; then EXTERNAL_HTTPS=available; fi
}

start_tunnel_after_http() {
  local want="$1" i health
  # The tunnel's startup handshake can fail permanently if HTTP starts late.
  for i in $(seq 1 15); do
    health="$(curl -fsS --max-time 3 http://127.0.0.1:8765/healthz 2>/dev/null || true)"
    if [[ "$health" == *"\"version\":\"$want\""* && "$health" == *'"status":"ok"'* ]]; then
      systemctl start "$TUNNEL"
      return $?
    fi
    sleep 2
  done
  return 1
}

verify_runtime() {
  local want="$1"
  local broker_want="${2:-$BROKER_NEW_SHA}"
  local i health ready running_sha pid
  for i in $(seq 1 15); do
    health="$(curl -fsS --max-time 3 http://127.0.0.1:8765/healthz 2>/dev/null || true)"
    ready="$(curl -fsS --max-time 3 http://127.0.0.1:8768/readyz 2>/dev/null || true)"
    if systemctl -q is-active "$SERVICE" &&
       systemctl -q is-active "$TUNNEL" &&
       [[ "$health" == *"\"version\":\"$want\""* ]] &&
       [[ "$health" == *'"status":"ok"'* ]] &&
       [[ "$ready" == "ready" ]]; then
      if [[ "$PAIRED" == 1 ]]; then
        systemctl -q is-active "$BROKER_SERVICE" || return 1
        pid="$(systemctl show --value --property=MainPID "$BROKER_SERVICE" 2>/dev/null || true)"
        [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
        running_sha="$(sha256sum -- "/proc/$pid/exe" 2>/dev/null || true)"
        if [[ "${running_sha%% *}" != "$broker_want" ]]; then
          return 1
        fi
      fi
      return 0
    fi
    sleep 2
  done
  return 1
}

fail_before_swap() {
  echo "self-deploy failed before swap: $1" >&2
  write_status failed "" "" "$(date -u +%FT%TZ)" || true
  exit 1
}

rollback_after_swap() {
  local reason="$1"
  local tmp
  echo "self-deploy verification failed: $reason; rolling back" >&2
  systemctl stop "$TUNNEL" || true
  systemctl stop "$SERVICE" || { write_status rollback_failed "" "" "$(date -u +%FT%TZ)"; exit 1; }
  if [[ "$PAIRED" == 1 ]]; then
    systemctl stop "$BROKER_SERVICE" || { write_status rollback_failed "" "" "$(date -u +%FT%TZ)"; exit 1; }
    if [[ ! -f "$BROKER_PREVIOUS" ]] || ! verify_hash_pin "$BROKER_PREVIOUS" "$BROKER_OLD_SHA"; then
      write_status rollback_failed "" "" "$(date -u +%FT%TZ)" || true
      exit 1
    fi
    local broker_restore
    broker_restore="$(mktemp /usr/local/libexec/.mcpshka-broker.rollback.XXXXXX)" || exit 1
    install -m 0755 "$BROKER_PREVIOUS" "$broker_restore" || exit 1
    mv -f "$broker_restore" "$BROKER_TARGET" || exit 1
    systemctl start "$BROKER_SERVICE" || exit 1
  fi
  if [[ ! -x "$PREVIOUS" ]] || [[ "$(version_of "$PREVIOUS" || true)" != "$EXPECTED_OLD" ]]; then
    write_status rollback_failed "$(version_of "$TARGET" || true)" "" "$(date -u +%FT%TZ)" || true
    exit 1
  fi
  tmp="$(mktemp /usr/local/bin/.mcpshka.rollback.XXXXXX)" || {
    write_status rollback_failed "$(version_of "$TARGET" || true)" "$EXPECTED_OLD" "$(date -u +%FT%TZ)" || true
    exit 1
  }
  if ! install -m 0755 "$PREVIOUS" "$tmp"; then
    rm -f "$tmp"
    write_status rollback_failed "$(version_of "$TARGET" || true)" "$EXPECTED_OLD" "$(date -u +%FT%TZ)" || true
    exit 1
  fi
  mv -f "$tmp" "$TARGET"
  if [[ -f /var/lib/mcpshka/state.mode ]] && [[ "$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$TARGET" state-store-support 2>/dev/null || true)" != "sqlite-v1" ]]; then
    python3 "$STATE_HELPER" deactivate /var/lib/mcpshka >/dev/null || { write_status rollback_failed "" "$EXPECTED_OLD" "$(date -u +%FT%TZ)"; exit 1; }
  fi
  systemctl restart "$SERVICE" || true
  start_tunnel_after_http "$EXPECTED_OLD" || true
  if verify_runtime "$EXPECTED_OLD" "$BROKER_OLD_SHA"; then
    write_status rolled_back "$EXPECTED_OLD" "$EXPECTED_OLD" "$(date -u +%FT%TZ)" || true
  else
    write_status rollback_failed "$(version_of "$TARGET" || true)" "$EXPECTED_OLD" "$(date -u +%FT%TZ)" || true
  fi
  exit 1
}

exec 9>"$LOCK"
if ! flock -n 9; then
  echo "another MCPshka deployment is active" >&2
  exit 75
fi

# Installed root gate enforces permanent retirement even for delayed launches.
python3 -I -S /usr/local/libexec/mcpshka-self-deploy check-fence --deployment-id "$ID" || exit 75

# Lock order matches candidate retirement: deployment, then private store.
python3 -I -S - <<'PY'
import os, stat
parent = "/var/lib/mcpshka-executor/candidates"
for path in ("/var/lib/mcpshka-executor", parent):
    s = os.lstat(path)
    if not stat.S_ISDIR(s.st_mode) or s.st_uid != 0 or stat.S_IMODE(s.st_mode) != 0o700:
        raise SystemExit("candidate store is unverified: HOLD")
fd = os.open(parent + "/.store.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
try:
    s = os.fstat(fd)
    if not stat.S_ISREG(s.st_mode) or s.st_uid != 0 or stat.S_IMODE(s.st_mode) != 0o600 or s.st_nlink != 1:
        raise SystemExit("candidate store lock is unverified: HOLD")
finally:
    os.close(fd)
PY
exec 8>>/var/lib/mcpshka-executor/candidates/.store.lock
flock -n 8 || { echo "candidate store busy: HOLD" >&2; exit 75; }

# A broker replacement must exclude concurrent Face ID/HA privileged writes.
# Root-owned lock, never a service-user grant or privilege bypass.
if [[ "$PAIRED" == 1 ]]; then
  exec 7>>/var/lib/mcpshka-executor/ha-automations.lock
  python3 -I -S - <<'PY'
import os,stat
s=os.fstat(7)
if not stat.S_ISREG(s.st_mode) or s.st_uid!=0 or s.st_nlink!=1 or stat.S_IMODE(s.st_mode)!=0o600:
    raise SystemExit("untrusted privileged task exclusion lock: HOLD")
PY
  flock -n 7 || { echo "privileged HA task is active: HOLD" >&2; exit 75; }
fi

# Give the MCP response enough time to reach the caller before the agent restarts.
sleep 3
write_status running

[[ -x "$CANDIDATE" ]] || fail_before_swap "candidate is not executable"
[[ -x "$TARGET" ]] || fail_before_swap "current binary is missing"
if [[ -n "$EXPECTED_OLD_SHA" ]]; then
  verify_hash_pin "$TARGET" "$EXPECTED_OLD_SHA" || fail_before_swap "current binary hash changed"
fi
[[ "$(version_of "$TARGET" || true)" == "$EXPECTED_OLD" ]] || fail_before_swap "current version changed"
systemctl -q is-active "$SERVICE" || fail_before_swap "service is not active"
systemctl -q is-active "$TUNNEL" || fail_before_swap "tunnel is not active"
observe_external_connectivity
write_status running

[[ -f "$AUTH_MIGRATOR" && ! -L "$AUTH_MIGRATOR" ]] || fail_before_swap "auth registry migrator is missing/unsafe"
python3 "$AUTH_MIGRATOR" prepare >/dev/null || fail_before_swap "auth registry preparation failed"

new_tmp="$(mktemp /usr/local/bin/.mcpshka.new.XXXXXX)" || fail_before_swap "cannot allocate install temp"
trap 'rm -f -- "$new_tmp"' EXIT
if ! stage_candidate_nofollow "$CANDIDATE" "$new_tmp"; then
  rm -f "$new_tmp"
  fail_before_swap "cannot safely stage candidate"
fi
if [[ -n "$EXPECTED_NEW_SHA" ]] && ! verify_hash_pin "$new_tmp" "$EXPECTED_NEW_SHA"; then
  rm -f "$new_tmp"
  fail_before_swap "staged candidate hash changed"
fi
[[ "$(version_of "$new_tmp" || true)" == "$EXPECTED_NEW" ]] || {
  rm -f "$new_tmp"
  fail_before_swap "staged candidate validation failed"
}
if [[ "$PAIRED" == 1 ]]; then
  [[ -f "$BROKER_CANDIDATE" && -x "$BROKER_TARGET" ]] || fail_before_swap "missing pinned broker candidate/current"
  verify_hash_pin "$BROKER_CANDIDATE" "$BROKER_NEW_SHA" || fail_before_swap "new broker hash mismatch"
  verify_hash_pin "$BROKER_TARGET" "$BROKER_OLD_SHA" || fail_before_swap "installed broker hash changed"
  broker_tmp="$(mktemp /usr/local/libexec/.mcpshka-broker.new.XXXXXX)" || fail_before_swap "cannot stage broker"
  stage_candidate_nofollow "$BROKER_CANDIDATE" "$broker_tmp" || fail_before_swap "broker staging failed"
  verify_hash_pin "$broker_tmp" "$BROKER_NEW_SHA" || fail_before_swap "staged broker mismatch"
  broker_previous_tmp="$(mktemp /usr/local/libexec/.mcpshka-broker.previous.XXXXXX)" || fail_before_swap "cannot backup broker"
  cp -p "$BROKER_TARGET" "$broker_previous_tmp" || fail_before_swap "cannot preserve broker"
  chmod 0755 "$broker_previous_tmp"
  verify_hash_pin "$broker_previous_tmp" "$BROKER_OLD_SHA" || fail_before_swap "broker rollback pin differs"
  mv -f "$broker_previous_tmp" "$BROKER_PREVIOUS"
fi
prev_tmp="$(mktemp /usr/local/bin/.mcpshka.previous.XXXXXX)" || fail_before_swap "cannot allocate rollback temp"
if ! cp -p "$TARGET" "$prev_tmp"; then
  rm -f "$prev_tmp"
  fail_before_swap "cannot copy rollback binary"
fi
chmod 755 "$prev_tmp"
[[ "$(version_of "$prev_tmp" || true)" == "$EXPECTED_OLD" ]] || {
  rm -f "$prev_tmp"
  fail_before_swap "rollback binary validation failed"
}
mv -f "$prev_tmp" "$PREVIOUS"

# Paired v2 release fences both writers before any binary swap.
if [[ "$PAIRED" == 1 ]]; then
  systemctl stop "$TUNNEL" || rollback_after_swap "cannot stop tunnel"
  systemctl stop "$SERVICE" || rollback_after_swap "cannot stop controller"
  systemctl stop "$BROKER_SERVICE" || rollback_after_swap "cannot stop broker"
  if systemctl -q is-active "$BROKER_SERVICE"; then rollback_after_swap "old broker still active"; fi
fi
# Change authority only while the server is quiescent.
if [[ "$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$new_tmp" state-store-support 2>/dev/null || true)" == "sqlite-v1" ]]; then
  systemctl stop "$SERVICE" || { if [[ "$PAIRED" == 1 ]]; then rollback_after_swap "cannot quiesce state"; else fail_before_swap "cannot quiesce state"; fi; }
  if ! python3 "$STATE_HELPER" activate /var/lib/mcpshka >/dev/null; then
    if [[ "$PAIRED" == 1 ]]; then rollback_after_swap "state migration failed"; fi
    # Activation validation failure must never switch an existing authority.
    rm -f "$new_tmp"
    systemctl start "$SERVICE" || true
    start_tunnel_after_http "$EXPECTED_OLD" || true
    if ! verify_runtime "$EXPECTED_OLD"; then
      write_status rollback_failed "$EXPECTED_OLD" "$EXPECTED_OLD" "$(date -u +%FT%TZ)" || true
      exit 1
    fi
    fail_before_swap "state migration failed"
  fi
fi
mv -f "$new_tmp" "$TARGET" || rollback_after_swap "controller binary replacement failed"
if [[ "$PAIRED" == 1 ]]; then
  mv -f "$broker_tmp" "$BROKER_TARGET" || rollback_after_swap "broker binary replacement failed"
  systemctl start "$BROKER_SERVICE" || rollback_after_swap "broker startup failed"
fi

if ! systemctl restart "$SERVICE"; then
  rollback_after_swap "service restart failed"
fi
start_tunnel_after_http "$EXPECTED_NEW" || rollback_after_swap "controller startup or tunnel start failed"
if ! verify_runtime "$EXPECTED_NEW"; then
  rollback_after_swap "runtime verification failed"
fi

observe_external_connectivity
write_status succeeded "$EXPECTED_NEW" "$EXPECTED_OLD" "$(date -u +%FT%TZ)"
# Retention workers recheck completion under the lock themselves.
# Releasing it here also lets a competing release defer this sweep safely.
flock -u 9
GATE_RETENTION="$(python3 -I -S /usr/local/libexec/mcpshka-self-deploy retain-gates --deployment-id "$ID")" || true
BIN_RETENTION="$("$BROKER_TARGET" release-cleanup-worker apply 2)" || true
[[ -n "$GATE_RETENTION" ]] || GATE_RETENTION='{"state":"HELD","deleted":null,"deleted_bytes":null}'
[[ -n "$BIN_RETENTION" ]] || BIN_RETENTION='{"state":"HELD","deleted":null,"deleted_bytes":null}'
CLEANUP_JSON="$(printf '{"gates":%s,"bin":%s}' "$GATE_RETENTION" "$BIN_RETENTION")"
write_status succeeded "$EXPECTED_NEW" "$EXPECTED_OLD" "$(date -u +%FT%TZ)"
echo "release retention: $CLEANUP_JSON"
echo "deployed mcpshka $EXPECTED_NEW; rollback=$EXPECTED_OLD"
