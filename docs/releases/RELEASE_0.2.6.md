# MCPshka 0.2.6 — honest approvals and effects, owner-safe worktrees

Date: 2026-10-27.

Fixes from the real ChatGPT → MCPshka → H96/HA/LG session of 2026-10-26/27 (BUG-20261027-139..148).

## Approved tasks
- Same `idempotency_key` + same pinned plan renews a lapsed approval (same id/hash/URL, fresh Face ID challenge); `approval_state`/deadline and `next_safe_action` tell awaiting from expired. A different plan under the key is a HOLD, not a silent stale replay. Renewal re-reads task and grant under both locks.
- Terminal effect is `applied` / `none` / `unknown`. `none` only when the worker's pin check failed; host observations never upgrade `unknown` to success (`observed=declared_state_present`, no-replay next action).
- Units declared by an executed plan are readable status-only (`service_status`/`service_list` `owned_by`).
- Reach preflight before the approval is spent; `webos_deploy` pins the TV Developer Mode port; webOS SSH uses `-T` with honest PTY diagnostics.
- An empty `systemctl` answer no longer marks the worker as ended.
- `admin_coverage(approved_task_execute)` names the per-plan Face ID route; `exec_start` interpreter denial hints `project_test`/`project_build`; `root_task_prepare` returns NO_CHANGES with the staging path.

## Fixed
- BUG-20261027-148: `project_repo_adopt` was always held — its before probe called `project_repo_preview` without session/path. Service-owned sibling worktrees (`/root/repos/<name>`) can be created again through the Face ID handover.
- BUG-20261027-149: sealed `project_test` of a registered Go tree failed in 0.2.5 — the preparation unit hid the trusted module cache. It is now bound read-only, and preparation gets one core so a tree plus its go.sum dependencies seals within the broker's 30 s request.
