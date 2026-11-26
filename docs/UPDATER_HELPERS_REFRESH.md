# Refreshing root updater helpers (0.1.153)

`/usr/local/libexec/mcpshka-state-store.py`, `mcpshka-state-backup.py` and
`mcpshka-release-probe.py` belong to the protected `updater` component. Neither
`project_deploy` (controller + broker) nor the coordinated upgrade replaces
them, and `broker_install` is disabled after service-user activation. Until a
Face ID broker-maintenance RPC exists (see `RELEASE_PROMOTION.md`, "Still
open"), they are refreshed by a human root operator with
`packaging/native/install-updater-helpers.py`.

Why 0.1.153 needs it: the installed state helper validated the database under
a fixed 15 s window (7.9 s at 16k records under deploy load rolled back the
first 0.1.152 deployment) and refused more than 10000 records, so
export/deactivate/migrate/backup were broken.

## Procedure

Run only after the release commit `C` is integrated into `main`, CI passed and
no deployment, integration or privileged task is active.

1. Root-owned snapshot of the reviewed source (no repository files executed):

   ```sh
   install -d -m 0700 /root/mcpshka-helpers-0.1.153
   git -C /root/repos/mcpshka archive C packaging/native/state-store.py \
     packaging/native/state-backup.py packaging/native/release-probe.py \
     packaging/native/install-updater-helpers.py \
     | tar -x -C /root/mcpshka-helpers-0.1.153 --strip-components=2 --no-same-owner
   chmod 0644 /root/mcpshka-helpers-0.1.153/*
   sha256sum /root/mcpshka-helpers-0.1.153/*   # compare with git/CI
   ```

2. Open the Face ID administrative session through MCP
   (`admin_session_prepare` → `admin_session_begin` → confirm Face ID →
   `admin_session_status`) and note `id`, `owner`, `generation`.

3. Review the plan; it lists only helpers whose bytes differ:

   ```sh
   python3 -I /root/mcpshka-helpers-0.1.153/install-updater-helpers.py --check \
     --version 0.1.153 --source /root/mcpshka-helpers-0.1.153
   ```

4. Apply exactly the reviewed digest as a detached bounded unit:

   ```sh
   systemd-run --unit=mcpshka-updater-helpers-0153 --wait --collect \
     -p RuntimeMaxSec=600 /usr/bin/python3 -I \
     /root/mcpshka-helpers-0.1.153/install-updater-helpers.py \
     --apply PLAN_SHA256 --version 0.1.153 --source /root/mcpshka-helpers-0.1.153 \
     --session-id ID --owner OWNER --generation GEN
   journalctl -u mcpshka-updater-helpers-0153 --no-pager
   ```

   The script takes the deployment lock, stores root-only previous copies and
   an append-only journal in `/var/lib/mcpshka-executor/updater-helpers/OP/`,
   replaces the helpers atomically and runs a read-only `state-store status`
   on the live authority. Any failure restores every helper
   (`ROLLED_BACK`). A changed snapshot or installed helper is HOLD.

5. Verify with `state_status` / `project_verify`. To revert later:
   `install-updater-helpers.py --rollback OP --session-id … --owner … --generation …`
   (only if the installed bytes are still the ones this operation wrote).

6. Remove `/root/mcpshka-helpers-0.1.153` and close the administrative session.

An UNKNOWN outcome (interrupted unit) is inspected from the journal directory;
never run a blind second `--apply`.
