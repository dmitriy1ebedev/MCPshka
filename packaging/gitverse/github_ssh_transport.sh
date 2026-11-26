#!/bin/sh
# Pinned, argument-safe Git SSH transport for the service-owned deploy key.
exec /usr/bin/ssh -F /dev/null -i /var/lib/mcpshka/git-ssh/identity \
  -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null \
  -o HostKeyAlgorithms=ssh-ed25519 \
  -o UserKnownHostsFile=/var/lib/mcpshka/git-ssh/known_hosts "$@"
