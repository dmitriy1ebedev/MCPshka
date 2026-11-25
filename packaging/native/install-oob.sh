#!/bin/sh
# Install MCPshka rescue + the out-of-band reverse-SSH path to the stand VPS.
# Idempotent. On the VPS only ~/.mcpshka-oob of the unprivileged build user
# is written: no root, no packages, no firewall, OpenVPN untouched.
set -eu
SRC=$(cd "$(dirname "$0")" && pwd)
VPS=${OOB_TARGET:-mcpshka-build@203.0.113.10}
KEY=${OOB_KEY:-/var/lib/mcpshka/ssh/compute-195-133-66-248}
PORT=${OOB_PORT:-42022}
[ "$(id -u)" = 0 ] || { echo "root required" >&2; exit 1; }
[ -f /usr/local/libexec/mcpshka-state-store.py ] || { echo "state store helper missing" >&2; exit 1; }
SSHV="ssh -o BatchMode=yes -o ConnectTimeout=10 -o UserKnownHostsFile=$KEY.known_hosts -i $KEY $VPS"

install -m 0755 -o root -g root "$SRC/rescue.py" /usr/local/libexec/mcpshka-rescue.py
install -m 0755 -o root -g root "$SRC/oob.py" /usr/local/libexec/mcpshka-oob.py
install -m 0644 -o root -g root "$SRC/../systemd/mcpshka-rescue.service" "$SRC/../systemd/mcpshka-rescue.timer" "$SRC/../systemd/mcpshka-oob-tunnel.service" /etc/systemd/system/
install -d -m 0755 /etc/mcpshka
printf 'OOB_TARGET=%s\nOOB_KEY=%s\nOOB_KNOWN_HOSTS=%s.known_hosts\nOOB_PORT=%s\n' "$VPS" "$KEY" "$KEY" "$PORT" > /etc/mcpshka/oob-tunnel.env
printf '{"paths":[{"name":"vps","kind":"reverse-ssh","target":"%s","key":"%s","known_hosts":"%s.known_hosts","port":%s},{"name":"tailscale","kind":"inbound"}]}\n' "$VPS" "$KEY" "$KEY" "$PORT" > /etc/mcpshka/oob.json

# VPS side: the build key may publish exactly one loopback port (it is
# "restrict" otherwise), and a probe key that may only run the forced
# command back here.
runuser -u mcpshka -- $SSHV "cd ~/.ssh && { [ -f authorized_keys.bak-oob ] || cp -p authorized_keys authorized_keys.bak-oob; } && sed 's/^restrict ssh-ed25519 \\(.*mcpshka-compute-[0-9.]*\\)\$/restrict,port-forwarding,permitlisten=\"127.0.0.1:$PORT\" ssh-ed25519 \\1/' authorized_keys > ak.new && [ \$(grep -c ssh-ed25519 ak.new) = \$(grep -c ssh-ed25519 authorized_keys) ] && chmod 600 ak.new && mv ak.new authorized_keys"
runuser -u mcpshka -- $SSHV 'umask 077; mkdir -p ~/.mcpshka-oob; [ -f ~/.mcpshka-oob/probe ] || ssh-keygen -q -t ed25519 -N "" -C mcpshka-oob-probe -f ~/.mcpshka-oob/probe'
PUB=$(runuser -u mcpshka -- $SSHV 'cat ~/.mcpshka-oob/probe.pub')
case "$PUB" in ssh-ed25519\ *) ;; *) echo "unexpected probe key" >&2; exit 1;; esac
HOSTKEY=$(cut -d' ' -f1,2 /etc/ssh/ssh_host_ed25519_key.pub)
printf '[127.0.0.1]:%s %s\n' "$PORT" "$HOSTKEY" | runuser -u mcpshka -- $SSHV 'umask 077; cat > ~/.mcpshka-oob/known_hosts'

LINE="from=\"127.0.0.1,::1\",restrict,command=\"/usr/bin/python3 /usr/local/libexec/mcpshka-oob.py serve vps\" $PUB"
AK=/root/.ssh/authorized_keys
if ! grep -qF "$PUB" "$AK"; then
  cp -p "$AK" "$AK.bak-oob-$(date +%Y%m%d%H%M%S)"
  printf '%s\n' "$LINE" >> "$AK"
fi

systemctl daemon-reload
systemctl enable --now mcpshka-oob-tunnel.service mcpshka-rescue.timer
sleep 5
python3 /usr/local/libexec/mcpshka-oob.py verify vps
