# GitVerse immutable snapshots and GigaIDE Cloud lane

## Current evidence and scope

GitVerse private repository `git@gitverse.ru:dmitriy_1ebedev/mcpshka.git`
was read successfully after account SSH-key enrollment on 2026-10-16.
Remote `refs/heads/main` existed at
`adb41c787bc9a48930c35efe7af44f695f0fa8ee`.
Dedicated key and pinned known_hosts exist under service-only
`/var/lib/mcpshka/gitverse`; do not export these files or put them into Git.
The pinned RSA host fingerprint is
`SHA256:IL4khwaSrT4uHCu9LTUW7dRJyMB5XkOJnFrP0nmdMXU`.

**First verified remote snapshot (2026-10-16):** source main commit
`609e09fade090df3988905b31c4b290166934eeb`, source tree
`4982f2c01be5aa9b53a0db9424ed43deb98f9c83`, snapshot commit
`454b59d2820476907d9395514a7b68e50ea4ab16`. Remote ref
`refs/heads/mcpshka-snapshots/609e09fade090df3988905b31c4b290166934eeb`
was created and verified via authenticated ls-remote readback; `main` was
not modified. The trial was run as a bounded single job; the result is **not**
evidence that GitVerse cloud CI or GigaIDE is installed or running.

Old `packaging/gitverse/push_snapshot.py` rewrites remote `main` with
force-with-lease. **Do not use it for the current integration.** New
`packaging/gitverse/snapshot_immutable.py` creates one deterministic,
parentless commit from a clean local main tree and publishes it to a new
`refs/heads/mcpshka-snapshots/<source SHA>` only. It will not overwrite an
existing differing ref, cannot push to remote main, validates SSH host key,
verifies remote ref readback and emits redacted machine-readable status.

`python3 -I packaging/gitverse/snapshot_immutable.py plan` is a zero-network
read-only preview. `publish` is a mutation: use approved bounded jobs,
one-writer semantics, idempotency identity and verify results before retry.
No remote branch deletion has been implemented. Never consider a Git snapshot
a release, agent approval, independent CI pass or permission to deploy.

## GigaIDE Cloud workspace

This is a browser-based development environment linked to GitVerse, distinct
from GigaCloud infrastructure hosting. Official start guide:
https://gitverse.ru/docs/gigaide-cloud-uc/gigaide-quick-start-uc

Prerequisites: owner login to Cloud.ru, nonnegative Cloud.ru account balance,
acceptance of the service offer, and a selected GitVerse repository.
Official docs describe limited monthly free allowance with **automatic
transition to paid billing when free resources are depleted**, so check
the billing dashboard before starting.

From GitVerse private repository choose **Code → Open in GigaIDE Cloud**;
select a dedicated branch, minimal CPU/RAM and start workspace. Only source
files from Git should be present: no production .env, Docker socket, HA data,
tunnel keys or local secrets. Run a first bounded acceptance:

1. Verify fetched Git tree and source digest, branch and Go toolchain.
2. Run `go test -p 1 -count=1 ./internal/systeminfo` and `git diff --check`
   on a short, isolated branch. Do not write directly to main.
3. Save source SHA, Go version, test result, resource use and runtime as an
   untrusted task receipt; compare against GitHub CI separately.
4. Stop workspace, check billing, and record limits. No GigaIDE workspace,
   subscription or paid resource has been created by MCPshka.

Next: provider-neutral durable dispatcher for GitVerse, MosHub, GigaIDE,
Codex, Claude Code and GigaChat with individual auth adapters,
source/branch scopes, budget admission, status/cancel/reconcile, and
artifact attestation. This lane does not override MCPshka P4 OOB gates.
