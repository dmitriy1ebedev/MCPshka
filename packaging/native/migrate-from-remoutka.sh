#!/usr/bin/env bash
set -euo pipefail
umask 077

[[ $EUID -eq 0 ]] || { echo "must run as root" >&2; exit 1; }
exec 9>/run/mcpshka-self-deploy.lock
flock -n 9 || { echo "deployment busy: HOLD" >&2; exit 75; }

export HOME=/root
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export GOPATH=/root/go
export GOMODCACHE=/root/go/pkg/mod
export GOCACHE=/root/.cache/go-build
export XDG_CACHE_HOME=/root/.cache
mkdir -p "$GOMODCACHE" "$GOCACHE"

OLD_REPO=/root/repos/remoutka
NEW_REPO=/root/repos/mcpshka
OLD_CFG=/etc/remoutka
NEW_CFG=/etc/mcpshka
OLD_STATE=/var/lib/remoutka
NEW_STATE=/var/lib/mcpshka
OLD_TUNNEL_STATE=/var/lib/remoutka-tunnel
NEW_TUNNEL_STATE=/var/lib/mcpshka-tunnel
BACKUP_ROOT=/var/backups/mcpshka
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP="$BACKUP_ROOT/$STAMP"
LOG=/var/log/mcpshka-migration.log

SOURCE_BIN="$NEW_REPO/bin/mcpshka"
SOURCE_UNIT="$NEW_REPO/packaging/systemd/mcpshka.service"
SOURCE_TUNNEL_UNIT="$NEW_REPO/packaging/tunnel/mcpshka-tunnel.service"
SOURCE_TUNNEL_CFG="$NEW_REPO/packaging/tunnel/tunnel-client.yaml"

repo_moved=0
state_moved=0
tunnel_state_moved=0
opt_moved=0
success=0

log() {
  printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" | tee -a "$LOG"
}

rollback_on_error() {
  rc=$?
  trap - ERR
  [[ $success -eq 1 ]] && exit "$rc"
  log "migration failed rc=$rc; restoring legacy service"
  systemctl stop mcpshka-tunnel.service >/dev/null 2>&1 || true
  systemctl stop mcpshka.service >/dev/null 2>&1 || true

  if [[ $repo_moved -eq 1 && -d "$NEW_REPO" && ! -e "$OLD_REPO" ]]; then mv "$NEW_REPO" "$OLD_REPO" || true; fi
  if [[ $state_moved -eq 1 && -d "$NEW_STATE" && ! -e "$OLD_STATE" ]]; then mv "$NEW_STATE" "$OLD_STATE" || true; fi
  if [[ $tunnel_state_moved -eq 1 && -d "$NEW_TUNNEL_STATE" && ! -e "$OLD_TUNNEL_STATE" ]]; then mv "$NEW_TUNNEL_STATE" "$OLD_TUNNEL_STATE" || true; fi
  if [[ $opt_moved -eq 1 && -d /opt/mcpshka && ! -e /opt/remoutka ]]; then
    old_target="$(readlink -f /usr/local/bin/tunnel-client 2>/dev/null || true)"
    mv /opt/mcpshka /opt/remoutka || true
    if [[ "$old_target" == /opt/mcpshka/* ]]; then
      new_target="$(printf '%s' "$old_target" | sed 's#^/opt/mcpshka/#/opt/remoutka/#')"
      ln -sfn "$new_target" /usr/local/bin/tunnel-client || true
    fi
  fi

  rm -rf "$NEW_CFG" || true
  rm -f /etc/systemd/system/mcpshka.service /etc/systemd/system/mcpshka-tunnel.service
  rm -f /usr/local/bin/mcpshka /usr/local/bin/mcpshka.previous
  systemctl daemon-reload || true
  systemctl enable remoutka.service >/dev/null 2>&1 || true
  systemctl start remoutka.service || true
  if [[ -f /etc/systemd/system/remoutka-tunnel.service && -f /etc/remoutka/tunnel.env ]]; then
    systemctl enable remoutka-tunnel.service >/dev/null 2>&1 || true
    systemctl start remoutka-tunnel.service || true
  fi
  log "legacy restore attempted; backup=$BACKUP"
  exit "$rc"
}
trap rollback_on_error ERR

[[ $EUID -eq 0 ]] || { echo "must run as root" >&2; exit 1; }

# Give systemd-run time to return to the caller, then cut off all competing
# remote jobs before touching the source tree.
sleep 2
systemctl stop remoutka-tunnel.service >/dev/null 2>&1 || true
systemctl stop remoutka.service
log "legacy ingress and jobs stopped"

python3 - <<'PY'
from pathlib import Path

base = Path("/root/repos")
candidates = [base/"mcpshka", base/"mspshka", base/"remoutka"]
existing = []
for p in candidates:
    if p.is_dir() and p not in existing:
        existing.append(p)
if len(existing) != 1:
    raise SystemExit("expected exactly one source repo, got: " + ", ".join(map(str, existing)))
root = existing[0]
self_rel = Path("packaging/native/migrate-from-remoutka.sh")

for p in list(root.rglob("*")):
    if not p.is_file() or ".git" in p.parts:
        continue
    try:
        if p.relative_to(root) == self_rel:
            continue
    except ValueError:
        pass
    try:
        s = p.read_text(encoding="utf-8")
    except (UnicodeDecodeError, OSError):
        continue
    n = s.replace("MSPSHKA", "MCPSHKA").replace("MSPshka", "MCPshka").replace("mspshka", "mcpshka")
    if n != s:
        p.write_text(n, encoding="utf-8")

for src, dst in [
    (root/"cmd"/"mspshka", root/"cmd"/"mcpshka"),
    (root/"packaging"/"systemd"/"mspshka.service", root/"packaging"/"systemd"/"mcpshka.service"),
    (root/"packaging"/"tunnel"/"mspshka-tunnel.service", root/"packaging"/"tunnel"/"mcpshka-tunnel.service"),
]:
    if src.exists():
        if dst.exists():
            raise SystemExit("duplicate target: " + str(dst))
        src.rename(dst)

target = base/"mcpshka"
if root != target:
    if target.exists():
        raise SystemExit("target repo already exists")
    root.rename(target)

bad = []
for p in target.rglob("*"):
    if not p.is_file() or ".git" in p.parts:
        continue
    try:
        if p.relative_to(target) == self_rel:
            continue
    except ValueError:
        pass
    try:
        s = p.read_text(encoding="utf-8")
    except (UnicodeDecodeError, OSError):
        continue
    for needle in ("MSPSHKA", "MSPshka", "mspshka"):
        if needle in s:
            bad.append((str(p), needle))
if bad:
    raise SystemExit("wrong-name remnants: " + repr(bad[:20]))
PY

cd "$NEW_REPO"
bash -n packaging/native/*.sh packaging/tunnel/*.sh
gofmt -w cmd/mcpshka internal
go test ./...
go vet ./...
git diff --check
! grep -Rqs --exclude-dir=.git --exclude-dir=bin --exclude=migrate-from-remoutka.sh -E 'MSPSHKA|MSPshka|mspshka' .
mkdir -p bin
rm -f bin/remoutka bin/mspshka bin/mcpshka
go build -trimpath -o bin/mcpshka ./cmd/mcpshka
[[ "$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$SOURCE_BIN" version)" == "mcpshka 0.1.a.18" ]]

git add -A
if ! git diff --cached --quiet; then
  git -c user.name='MCPshka Builder' -c user.email='mcpshka@localhost' commit -m 'rename project to MCPshka'
fi
[[ -z "$(git status --porcelain)" ]]
log "MCPshka source tested and committed"

[[ -x "$SOURCE_BIN" ]] || { echo "missing new binary" >&2; exit 1; }
[[ "$(python3 -I -S /usr/local/libexec/mcpshka-release-probe.py "$SOURCE_BIN" version)" == mcpshka* ]] || { echo "invalid MCPshka binary" >&2; exit 1; }
[[ -f "$OLD_CFG/remoutka.env" ]] || { echo "missing legacy env" >&2; exit 1; }
[[ -d "$NEW_REPO" ]] || { echo "new repo path missing" >&2; exit 1; }
[[ ! -e "$NEW_STATE" ]] || { echo "new state path already exists" >&2; exit 1; }

mkdir -p "$BACKUP_ROOT" "$BACKUP"
chmod 700 "$BACKUP_ROOT" "$BACKUP"
cp -a "$OLD_CFG" "$BACKUP/etc-remoutka"
[[ -f /etc/systemd/system/remoutka.service ]] && cp -a /etc/systemd/system/remoutka.service "$BACKUP/remoutka.service"
[[ -f /etc/systemd/system/remoutka-tunnel.service ]] && cp -a /etc/systemd/system/remoutka-tunnel.service "$BACKUP/remoutka-tunnel.service"
[[ -x /usr/local/bin/remoutka ]] && cp -a /usr/local/bin/remoutka "$BACKUP/remoutka"
[[ -x /usr/local/bin/remoutka.previous ]] && cp -a /usr/local/bin/remoutka.previous "$BACKUP/remoutka.previous"
git -C "$NEW_REPO" rev-parse HEAD >"$BACKUP/repo-head" 2>/dev/null || true
printf '%s\n' "$BACKUP" >"$BACKUP_ROOT/latest"
chmod 600 "$BACKUP_ROOT/latest"
log "backup complete: $BACKUP"

mkdir -p "$NEW_CFG"
chmod 700 "$NEW_CFG"
sed -e 's/^REMOUTKA_/MCPSHKA_/' -e 's#/var/lib/remoutka#/var/lib/mcpshka#g' -e 's#/etc/remoutka#/etc/mcpshka#g'   "$OLD_CFG/remoutka.env" >"$NEW_CFG/mcpshka.env"
chmod 600 "$NEW_CFG/mcpshka.env"
[[ -f "$OLD_CFG/tunnel.env" ]] && install -m 0600 "$OLD_CFG/tunnel.env" "$NEW_CFG/tunnel.env"
install -m 0600 "$SOURCE_TUNNEL_CFG" "$NEW_CFG/tunnel-client.yaml"

token="$(sed -n 's/^MCPSHKA_TOKEN=//p' "$NEW_CFG/mcpshka.env" | head -1)"
[[ -n "$token" ]] || { echo "MCPSHKA_TOKEN missing" >&2; exit 1; }
printf 'Bearer %s\n' "$token" >"$NEW_CFG/mcp-auth-header"
chmod 600 "$NEW_CFG/mcp-auth-header"

install -m 0755 "$SOURCE_BIN" /usr/local/bin/mcpshka
[[ -x /usr/local/bin/remoutka ]] && install -m 0755 /usr/local/bin/remoutka /usr/local/bin/mcpshka.previous
install -m 0644 "$SOURCE_UNIT" /etc/systemd/system/mcpshka.service
install -m 0644 "$SOURCE_TUNNEL_UNIT" /etc/systemd/system/mcpshka-tunnel.service
systemd-analyze verify "$SOURCE_UNIT" "$SOURCE_TUNNEL_UNIT" >/dev/null

if [[ -d /opt/remoutka && ! -e /opt/mcpshka ]]; then
  old_target="$(readlink -f /usr/local/bin/tunnel-client 2>/dev/null || true)"
  mv /opt/remoutka /opt/mcpshka
  opt_moved=1
  if [[ "$old_target" == /opt/remoutka/* ]]; then
    new_target="$(printf '%s' "$old_target" | sed 's#^/opt/remoutka/#/opt/mcpshka/#')"
    ln -sfn "$new_target" /usr/local/bin/tunnel-client
  fi
fi

systemctl daemon-reload
log "new files staged; switching services"

systemctl stop remoutka-tunnel.service >/dev/null 2>&1 || true
systemctl stop remoutka.service

if [[ -d "$OLD_STATE" ]]; then mv "$OLD_STATE" "$NEW_STATE"; state_moved=1; fi
if [[ -d "$OLD_TUNNEL_STATE" ]]; then mv "$OLD_TUNNEL_STATE" "$NEW_TUNNEL_STATE"; tunnel_state_moved=1; fi

systemctl daemon-reload
systemctl enable mcpshka.service >/dev/null
systemctl start mcpshka.service
systemctl --quiet is-active mcpshka.service

for _ in $(seq 1 20); do
  curl -fsS --max-time 1 http://127.0.0.1:8765/healthz >/tmp/mcpshka-health.json 2>/dev/null && break
  sleep 0.5
done
grep -q '"name":"MCPshka"' /tmp/mcpshka-health.json
grep -q '"version":"0.1.a.18"' /tmp/mcpshka-health.json

systemctl enable mcpshka-tunnel.service >/dev/null
systemctl start mcpshka-tunnel.service
systemctl --quiet is-active mcpshka-tunnel.service
for _ in $(seq 1 40); do
  [[ "$(curl -fsS --max-time 1 http://127.0.0.1:8768/readyz 2>/dev/null || true)" == "ready" ]] && break
  sleep 0.5
done
[[ "$(curl -fsS --max-time 2 http://127.0.0.1:8768/readyz)" == "ready" ]]

systemctl disable remoutka-tunnel.service >/dev/null 2>&1 || true
systemctl disable remoutka.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/remoutka.service /etc/systemd/system/remoutka-tunnel.service
rm -f /usr/local/bin/remoutka /usr/local/bin/remoutka.previous
rm -rf "$OLD_CFG"
systemctl daemon-reload
systemctl reset-failed remoutka.service remoutka-tunnel.service >/dev/null 2>&1 || true

cat >"$NEW_STATE/migration.json" <<EOF
{"from":"Remoutka","to":"MCPshka","version":"0.1.a.18","backup":"$BACKUP","completed_at":"$(date -u +%FT%TZ)"}
EOF
chmod 600 "$NEW_STATE/migration.json"

success=1
trap - ERR
log "DONE MCPshka 0.1.a.18; backup=$BACKUP"
