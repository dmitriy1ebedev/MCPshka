# 0.1.138 — Native administrative archival

The authenticated administrative owner can prepare, execute, inspect and
reconcile a fixed `bootstrap-110` archive through `admin_archive_prepare`,
`admin_archive_execute`, `admin_archive_status` and `admin_archive_reconcile`.
Calls take `session_id` and `scope`; mutations also require `idempotency_key`.
One active Face ID work session covers the related calls without a new prompt.

Clients with a cached tool schema can use the authenticated in-process
`exec_start(command="mcpshka", args=["admin-archive", JSON], idempotency_key=KEY)`
route, with JSON fields `action`, `session_id`, `scope`. Actions are the four names
above without the `admin_archive_` prefix. The bridge checks the target tool's
scope and binds the authenticated owner before consulting any cached receipt;
it returns a completed job receipt whose bounded output contains the archive
status. Use a new status retry key for each fresh observation. This route never
launches a caller command or accepts an owner override.

The catalogue pins the two bootstrap rollback files, their SHA-256 hashes and
exact modes, the source beneath release authority, and the destination beneath
the executor. Callers cannot select paths or commands. Archival preserves the
files and uses atomic no-replace rename; it neither deletes rollback material nor
grants release authority. An archive already moved by an independent operator is
reported as `observed_existing_archive`, never as an effect performed by this API.

Immutable root-owned PREPARED/DISPATCHED/RUNNING/HELD/VERIFIED events bind the
operation to owner, session, grant generation and catalogue. Dispatch is recorded
before launching a bounded independent systemd worker. The worker rechecks the
grant under administrative, release-admission, deployment and HA locks. It checks
for existing deployment launchers before and after effects. A repeated dispatch
observes the original intent. After a lost reply or restart, status checks the
same worker and exact namespace; reconciliation can record an independently
verified archive after the worker exits, without replaying the move. Conflicting
or unarchived state remains HOLD/UNKNOWN and preserves all earlier evidence.

The permanent broker retains its existing capabilities and mount restrictions.
The worker uses one writable `/var/lib` mount so rename does not cross bind
mounts. A bounded, rechecked inventory makes every existing unrelated child
inaccessible inside that worker. Its namespace still permits creating new entries
directly under `/var/lib`: safety there relies on the fixed compiled operation
without caller paths or commands. No generic filesystem writer is exposed.

Tests cover owner-bound cached receipts, stale generations/revocation, cancelled
work, repeated intent, post-rename uncertainty, changed bytes/modes, special bits,
symlinks/hardlinks, immutable events, mount inventory and legacy launchers. A real
bounded PID1 fixture on H96 reproduced EXDEV with separate bind mounts, then
verified rename with the common mount and rejected write/unlink/rename against
an unrelated sibling. Go CI and live API acceptance are required before treating
this release as deployed. Push notifications and new Codex sessions remain open.
