#!/usr/bin/env bash
# MCP-173 v2: upgrade cached MOTD to timer-driven refresh; reversible.
# ROOT=/tmp/stage is available for safe offline tests. Never restarts SSH.
set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

ROOT=${ROOT:-}
SRC=$(cd "$(dirname "$0")" && pwd)
HOOK="$ROOT/etc/update-motd.d/32-router-dashboard"
REFRESH="$ROOT/usr/local/libexec/router-motd-refresh"
RENDER="$ROOT/usr/local/libexec/router-motd-compact.awk"
UNITDIR="$ROOT/etc/systemd/system"
SERVICE="$UNITDIR/router-motd-refresh.service"
TIMER="$UNITDIR/router-motd-refresh.timer"
BACKUP_BASE="$ROOT/var/backups/router-motd"
LATEST="$BACKUP_BASE/mcp173-v2-latest"

fail() { printf 'MOTD v2 error: %s\n' "$*" >&2; exit 1; }
atomic_install() {
  local mode=$1 source=$2 target=$3 temp
  mkdir -p "${target%/*}" || return 1
  temp="${target%/*}/.${target##*/}.mcp173-new.$$"
  install -m "$mode" "$source" "$temp" || return 1
  mv -f "$temp" "$target"
}
backup_one() {
  local source=$1 key=$2 b=$3
  if [[ -e $source ]]; then cp -p "$source" "$b/$key"; else : > "$b/$key.absent"; fi
}
restore_one() {
  local target=$1 key=$2 b=$3
  if [[ -f "$b/$key.absent" ]]; then rm -f "$target"; else
    [[ -f "$b/$key" ]] || return 1
    atomic_install "$(stat -c %a "$b/$key")" "$b/$key" "$target"
  fi
}
restore_from() {
  local b=$1 prev_enabled= prev_active=
  if [[ -z $ROOT ]]; then systemctl disable --now router-motd-refresh.timer >/dev/null 2>&1 || :; fi
  restore_one "$HOOK" hook "$b"
  restore_one "$REFRESH" refresh "$b"
  restore_one "$RENDER" render "$b"
  restore_one "$SERVICE" service "$b"
  restore_one "$TIMER" timer "$b"
  if [[ -z $ROOT ]]; then
    systemctl daemon-reload
    [[ -f $b/previous-enabled ]] && prev_enabled=$(cat "$b/previous-enabled")
    [[ -f $b/previous-active ]] && prev_active=$(cat "$b/previous-active")
    [[ $prev_enabled == enabled ]] && systemctl enable router-motd-refresh.timer >/dev/null
    [[ $prev_active == active ]] && systemctl start router-motd-refresh.timer
  fi
}
apply_changes() {
  atomic_install 0755 "$SRC/router-motd-refresh" "$REFRESH" || return 1
  atomic_install 0644 "$SRC/router-motd-compact.awk" "$RENDER" || return 1
  atomic_install 0755 "$SRC/router-motd-login" "$HOOK" || return 1
  atomic_install 0644 "$SRC/router-motd-refresh.service" "$SERVICE" || return 1
  atomic_install 0644 "$SRC/router-motd-refresh.timer" "$TIMER" || return 1
  if [[ -z $ROOT ]]; then
    systemctl daemon-reload || return 1
    systemctl enable --now router-motd-refresh.timer || return 1
    systemctl is-enabled --quiet router-motd-refresh.timer || return 1
    systemctl is-active --quiet router-motd-refresh.timer || return 1
    # Populate the new cached layout before accepting installation.
    systemctl start router-motd-refresh.service || return 1
  fi
  return 0
}
apply() {
  [[ -f $HOOK ]] || fail "existing MCP-173 login hook is missing"
  grep -qF 'router-motd-login (MCP-173)' "$HOOK" || fail "hook is not the previously installed MCP-173"
  sh -n "$SRC/router-motd-login" || fail "invalid login script"
  bash -n "$SRC/router-motd-refresh" || fail "invalid refresh script"
  [[ -r $SRC/router-motd-refresh.service && -r $SRC/router-motd-refresh.timer && -r $SRC/router-motd-compact.awk ]] || fail "missing unit files"
  # Validate the broker's writable directory mounts before creating backups or
  # modifying anything. Atomic replacement needs temporary sibling files.
  if [[ -z $ROOT ]]; then
    local dir probe
    for dir in "${HOOK%/*}" "${REFRESH%/*}" "$UNITDIR" "$BACKUP_BASE"; do
      [[ -d $dir ]] || fail "missing writable directory $dir"
      probe="$dir/.mcp173-write-probe.$$"
      if ! ( : > "$probe" ) 2>/dev/null; then fail "write scope unavailable: $dir"; fi
      rm -f "$probe" || fail "cannot remove write probe from $dir"
    done
  fi
  if cmp -s "$SRC/router-motd-login" "$HOOK" &&
     cmp -s "$SRC/router-motd-refresh" "$REFRESH" &&
     cmp -s "$SRC/router-motd-compact.awk" "$RENDER" &&
     cmp -s "$SRC/router-motd-refresh.service" "$SERVICE" &&
     cmp -s "$SRC/router-motd-refresh.timer" "$TIMER"; then
    if [[ -n $ROOT ]] || { systemctl is-active --quiet router-motd-refresh.timer &&
                           systemctl is-enabled --quiet router-motd-refresh.timer; }; then
      echo "MOTD v2 already installed"; return 0
    fi
  fi
  local b="$BACKUP_BASE/mcp173-v2-$(date +%Y%m%d-%H%M%S)-$$"
  mkdir -p "$b" || fail "cannot create backup directory"
  backup_one "$HOOK" hook "$b"
  backup_one "$REFRESH" refresh "$b"
  backup_one "$RENDER" render "$b"
  backup_one "$SERVICE" service "$b"
  backup_one "$TIMER" timer "$b"
  if [[ -z $ROOT ]]; then
    systemctl is-enabled router-motd-refresh.timer 2>/dev/null > "$b/previous-enabled" || :
    systemctl is-active router-motd-refresh.timer 2>/dev/null > "$b/previous-active" || :
  fi
  if ! apply_changes; then
    echo "MOTD v2 apply failed; restoring previous configuration" >&2
    restore_from "$b" || echo "MOTD v2 rollback also failed; backup: $b" >&2
    return 1
  fi
  ln -sfn "$b" "$LATEST"
  printf 'MOTD v2 installed; timer active (or staged); backup: %s\n' "$b"
}
rollback() {
  [[ -L $LATEST ]] || fail "no v2 backup found"
  local b; b=$(readlink -f "$LATEST")
  [[ -d $b ]] || fail "backup target not found"
  restore_from "$b" || fail "rollback failed; inspect backup $b"
  rm -f "$LATEST"
  printf 'MOTD v2 reverted from %s\n' "$b"
}
status() {
  if [[ -f $HOOK ]] && cmp -s "$HOOK" "$SRC/router-motd-login"; then
    echo "MOTD v2 code: installed"
  else
    echo "MOTD v2 code: not installed"
  fi
  [[ -L $LATEST ]] && printf 'Previous configuration: %s\n' "$(readlink -f "$LATEST")"
  if [[ -z $ROOT ]]; then systemctl is-enabled router-motd-refresh.timer || :; systemctl is-active router-motd-refresh.timer || :; fi
}
case ${1:-} in
  apply) apply ;;
  rollback) rollback ;;
  status) status ;;
  *) fail "usage: bash install-v2.sh apply|rollback|status" ;;
esac
