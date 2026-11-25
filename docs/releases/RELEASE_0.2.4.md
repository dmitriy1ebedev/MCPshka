# MCPshka 0.2.4 — lease recovery for any project and any chat

Date: 2026-10-26.

## Lease lifecycle (BUG-20261026-135, Codex + Claude review)
- Administrative recovery works for every registered project, not only the MCPshka repository: `project_lease_admin_recovery_preview` no longer requires the fixed main repository and reports machine-readable `blockers`, `next_action` and `disposition`.
- Stale release: a holder bound to a local process is released as soon as its exact PID/start-time/boot is proven gone, with no wait. A remote chat holder (no process on the host) is released under the owner's Face ID after 30 min without a heartbeat, or at once when its agent_task unit has ended, if no project job, deployment or process uses the worktree. Deleting the record fences the old token; uncommitted files stay in place. The old fixed 2 h wait is gone.
- Release and recovery are journaled (CAS with an audit receipt) and idempotent: repeating the same plan returns the recorded outcome; a changed lease asks for a fresh preview.
- Dirty worktrees are preserved as a recovery snapshot ref before a sealed recovery transfer instead of blocking it.
- `project_lease_resume`: voluntary token handoff from a stopped holder to a new chat (new execution, generation +1).
- Failure classification: OWNER_BUSY, FAILED_VERIFIED, POLICY_DENIED, stale receipts, timeouts and lost replies carry a disposition (transient / needs approval / no authority / blocker) and a next safe action.
- The isolated test executor pin (`pin.lease_id`) is reported as its own identity, not as the writer lease.

## Fixed
- BUG-20261026-134: a chatty sealed suite died of SIGPIPE (exit 141): its stdout was the 8 KiB evidence writer, which fails on overflow. The suite output now keeps the first 2 KiB and the last 5 KiB and never fails a write.
- BUG-20261026-136: a sealed `project_test` on a worktree nested deeper than `/root/repos/<name>` returned a bare POLICY_DENIED; it now names the `/root/repos/<project>-<name>` layout or `packages/run`.
- BUG-20261026-130: `codex_sessions_list` includes `agent_task` (exec) sessions; "Selected model is at capacity" is reported as a capacity error instead of `exit status 1`.

## Known
- An unknown remote owner can still take over only through the stale path above or through a voluntary resume; root-sealed recovery still needs an independent 0500 fence.
- Root-only lease tests run in CI (as root), not in the local unprivileged sandbox.
