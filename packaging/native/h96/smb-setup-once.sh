#!/usr/bin/env bash
# One-time SMB file share setup for H96 Max X3 (Debian, Samba).
set -Eeuo pipefail
umask 077

if (( EUID != 0 )); then
  echo 'Run as root (Termius: sudo -i, then bash /path/to/script).' >&2
  exit 1
fi
if [[ "$(hostname -s)" != 'armbian' ]]; then
  echo 'STOP: unexpected host name; nothing changed.' >&2
  exit 1
fi
if [[ -e /etc/samba/smb.conf ]] && ! grep -qFx '# managed: h96-smb-files-v1' /etc/samba/smb.conf; then
  echo 'STOP: an existing Samba configuration is present; will not overwrite it.' >&2
  exit 2
fi
if [[ -e /srv/samba/share && ( ! -d /srv/samba/share || -L /srv/samba/share ) ]]; then
  echo 'STOP: share path is not a regular directory.' >&2
  exit 2
fi
if ! id -u smbfiles >/dev/null 2>&1; then
  useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin smbfiles
fi
mkdir -p /srv/samba/share
chown smbfiles:smbfiles /srv/samba/share
chmod 0700 /srv/samba/share
mkdir -p /etc/samba
if [[ ! -e /etc/samba/smb.conf ]]; then
  cat > /etc/samba/smb.conf <<'CONFIG'
# managed: h96-smb-files-v1
[global]
    workgroup = WORKGROUP
    server role = standalone server
    server string = H96 Files
    security = user
    map to guest = Never
    restrict anonymous = 2
    server min protocol = SMB2_10
    smb ports = 445
    disable netbios = yes
    interfaces = lo eth0 tailscale0
    bind interfaces only = yes
    hosts allow = 127.0.0.1 192.168.0.0/24 100.64.0.0/10
    hosts deny = ALL

[Files]
    path = /srv/samba/share
    browsable = yes
    read only = no
    guest ok = no
    valid users = smbfiles
    create mask = 0600
    directory mask = 0700
CONFIG
  chmod 0600 /etc/samba/smb.conf
fi

# Existing configuration is staged before installation so package startup
# never publishes the distribution's broad default share.
if ! dpkg-query -W -f='${db:Status-Abbrev}' samba 2>/dev/null | grep -q '^ii'; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    -o Dpkg::Options::='--force-confold' samba
fi
command -v testparm >/dev/null || { echo 'Missing testparm'; exit 3; }
testparm -s /etc/samba/smb.conf >/dev/null
printf '\nSet a NEW password for the SMB-only user smbfiles (not your SSH password):\n'
smbpasswd -a smbfiles
smbpasswd -e smbfiles
systemctl disable --now nmbd.service >/dev/null 2>&1 || true
# Armbian ships DefaultTimeoutStartSec=10s; on a cold boot under load smbd needs
# longer (15.11: power loss → smbd timeout → failed unit, MCP-193).
install -d -m 755 /etc/systemd/system/smbd.service.d
printf '[Service]\nTimeoutStartSec=90s\nRestart=on-failure\nRestartSec=15s\n' > /etc/systemd/system/smbd.service.d/10-mcpshka-boot.conf
systemctl daemon-reload
systemctl enable --now smbd.service
systemctl restart smbd.service
systemctl is-active --quiet smbd.service || { echo 'smbd is not active'; exit 4; }
printf '\nSMB installation completed.\n'
printf 'LAN:       smb://192.168.77.10/Files\n'
printf 'Tailscale: smb://100.77.0.10/Files\n'
printf 'User:      smbfiles\n'
printf 'Service:   %s\n' "$(systemctl is-active smbd.service)"
ss -ltn '( sport = :445 )' || true
