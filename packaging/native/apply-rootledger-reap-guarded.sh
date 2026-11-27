#!/bin/bash
# Root-only, fixed-scope break-glass entry; no arbitrary Docker/container arguments.
set -euo pipefail
[[ $# -eq 0 && $EUID -eq 0 ]] || { echo "Run as root without arguments"; exit 2; }
src=/root/repos/mcpshka/bin/codex-autonomy-20261008-wt/packaging/native/rootledger-reap.py
dst=/usr/local/libexec/mcpshka-rootledger-reap.py
expected=119506b6a2c8065c9431906986e45ee2ebbe8b70bed3b800a2c236bc161c73ac
tmp=$(mktemp /usr/local/libexec/.mcpshka-rootledger-reap.XXXXXX)
trap 'rm -f "$tmp"' EXIT
install -o root -g root -m 0755 "$src" "$tmp"
printf '%s  %s\n' "$expected" "$tmp" | sha256sum --check --status
if [[ -e "$dst" || -L "$dst" ]]; then
    echo "HOLD: helper already exists; reconcile previous operation, do not repeat"
    exit 1
fi
ln "$tmp" "$dst"

# Each worker is detached from this terminal and has an independent recovery hook.
for operation in smoke apply; do
    case "$operation" in
        smoke) run_id=2026100800000063 ;;
        apply) run_id=2026100800000064 ;;
    esac
    systemd-run --unit="mcpshka-rootledger-reap-$operation-$run_id" --wait --pipe --collect \
        --property=Type=oneshot --property=TimeoutStartSec=300s --property=TimeoutStopSec=180s \
        "--property=ExecStopPost=/usr/bin/python3 $dst recover $run_id" \
        --property=MemoryMax=256M --property=CPUQuota=50% --property=TasksMax=64 \
        --property=UMask=0077 --property=NoNewPrivileges=yes \
        /usr/bin/python3 "$dst" "$operation" "$run_id"
done
cat /var/lib/mcpshka-rootledger-reap/2026100800000064/status.json
