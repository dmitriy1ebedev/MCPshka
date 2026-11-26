#!/usr/bin/env bash
# MCPshka / H96 Max X3: read-only whole-system SMB view + dedicated writable workspace.
# Run as root on armbian. Does not modify networking, VPN, Docker, or existing projects.
set -Eeuo pipefail
umask 077

CONF=/etc/samba/smb.conf
BASE=/srv/samba
SHARE=/srv/samba/share
WORK=/srv/samba/workspace
MARKER='# managed: h96-smb-files-v1'

fail() { printf 'STOP: %s\n' "$*" >&2; exit 1; }
(( EUID == 0 )) || fail 'root required'
[[ "$(hostname -s)" == armbian ]] || fail 'unexpected hostname'
[[ -f "$CONF" && ! -L "$CONF" ]] || fail 'Samba configuration not a regular file'
grep -Fxq "$MARKER" "$CONF" || fail 'Samba config is not the previously managed configuration; manual review needed'
id smbfiles >/dev/null 2>&1 || fail 'SMB user smbfiles does not exist'
[[ -d "$BASE" && ! -L "$BASE" && -d "$SHARE" && ! -L "$SHARE" ]] || fail 'unexpected Samba folder layout'
command -v testparm >/dev/null || fail 'testparm missing'
command -v systemctl >/dev/null || fail 'systemctl missing'
grep -Eq '^[[:space:]]*bind interfaces only[[:space:]]*=[[:space:]]*yes[[:space:]]*$' "$CONF" || fail 'SMB interface restrictions are missing'
grep -Eq '^[[:space:]]*hosts allow[[:space:]]*=' "$CONF" || fail 'SMB allowlist is missing'
grep -Eq '^[[:space:]]*hosts deny[[:space:]]*=' "$CONF" || fail 'SMB denylist is missing'
testparm -s "$CONF" >/dev/null || fail 'current Samba configuration did not validate'

if grep -qFx '# managed: h96-smb-full-view-v1' "$CONF"; then
  printf 'Already configured; running checks.\n'
else
  if grep -Eq '^[[:space:]]*\[(System|Workspace)\][[:space:]]*$' "$CONF"; then
    fail 'System or Workspace share already exists; do not overwrite'
  fi
  if [[ -e "$WORK" || -L "$WORK" ]]; then
    fail 'workspace path already exists; inspect before modifying it'
  fi
  BASE_ORIGINAL_MODE=$(stat -c '%a' "$BASE")
  BACKUP="/var/backups/smb.conf.before-full-view.$(date -u +%Y%m%dT%H%M%SZ).$$"
  install -d -m 0700 /var/backups
  cp -a -- "$CONF" "$BACKUP"
  printf 'Backup: %s\n' "$BACKUP"
  CHANGED=0
  rollback() {
    result=$?
    trap - ERR
    if (( CHANGED )); then
      chmod "$BASE_ORIGINAL_MODE" "$BASE" || true
      rmdir "$WORK" 2>/dev/null || true
      printf 'ERROR: reverting Samba configuration from backup\n' >&2
      cp -a -- "$BACKUP" "$CONF" || true
      systemctl restart smbd.service || true
    fi
    exit "$result"
  }
  trap rollback ERR

  CHANGED=1
  # New writable directory is owned by the Samba-only account.
  # Existing repositories and production data are intentionally not writable.
  install -d -m 0700 -o smbfiles -g smbfiles "$WORK"
  chmod 0711 "$BASE"   # allow smbfiles to traverse to its own directories

  # The System share uses Samba's root impersonation *only in a read-only share*.
  # It can expose private keys, tokens and /etc/shadow. Treat SMB password as sensitive.
  cat >>"$CONF" <<'SAMBA_CONFIG'

# managed: h96-smb-full-view-v1
[System]
    comment = Full Linux filesystem - READ ONLY
    path = /
    browseable = yes
    valid users = smbfiles
    guest ok = no
    read only = yes
    admin users = smbfiles

[Workspace]
    comment = Safe writable workspace (not production code)
    path = /srv/samba/workspace
    browseable = yes
    valid users = smbfiles
    guest ok = no
    read only = no
    force user = smbfiles
    create mask = 0600
    directory mask = 0700
SAMBA_CONFIG
  # Explicit overlay IP binding when available (some Samba builds miss tailscale0).
  if ip -4 addr show dev tailscale0 2>/dev/null | grep -Fq '100.77.0.10/'; then
    sed -i -E '/^[[:space:]]*interfaces[[:space:]]*=[[:space:]]*lo eth0 tailscale0[[:space:]]*$/s@$@ 100.77.0.10/32@' "$CONF"
  fi
  testparm -s "$CONF" >/dev/null
  systemctl restart smbd.service
  systemctl is-active --quiet smbd.service
  trap - ERR
fi

# Safe non-mutating checks plus a transient round-trip as the SMB account.
testparm -s "$CONF" >/dev/null
systemctl is-active --quiet smbd.service || fail 'smbd not active'
runuser -u smbfiles -- sh -c 'test -w /srv/samba/workspace && test -x /srv/samba && test -w /srv/samba/share' || fail 'workspace/share permissions not sufficient'

echo 'SMB CONFIGURED: System=/ (read-only); Workspace=/srv/samba/workspace (read-write); Files=/srv/samba/share (read-write).'
echo 'LAN:       smb://192.168.77.10'
echo 'Tailscale: smb://100.77.0.10 (requires Tailscale connectivity and port 445 listener)'
echo 'Login:     smbfiles'
ss -ltn '( sport = :445 )' || true
if ss -H -ltn '( sport = :445 )' | grep -Fq '100.77.0.10:445'; then
  echo 'Tailscale SMB listener: verified'
else
  echo 'WARNING: No 100.77.0.10:445 listener visible; LAN may work while Tailscale needs additional diagnosis.' >&2
fi
