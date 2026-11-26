# MCPshka 0.1.153

Controller-only release on top of 0.1.152 (`claude/workbench-wiring`, PR #67).
No broker, auth, policy or approval contract changes; the root updater helpers
change in source and are installed separately (`docs/UPDATER_HELPERS_REFRESH.md`).

## Fixes

- **State helper scales with the database.** `state-store.py` validated under
  a fixed 15 s window (7.9 s at 16k records rolled back the first 0.1.152
  deploy) and refused >10000 records, breaking export/deactivate/migrate and
  backup. Window is now 15 s + 2 s/MiB (cap 300 s); record ceiling 250000.
  Installing it requires the Face ID-pinned root procedure.
- **Lost admin recovery responses are replayable.** Repeating
  `project_lease_admin_recover` with the same key and plan returns the
  journaled successor while it is untouched; nothing rotates twice. A PENDING
  contract receipt of this replay-safe tool re-runs instead of UNKNOWN.
- **Who holds the repository.** Recovery preview/errors list each live process
  referencing the sealed repository (pid, uid, command, reference, path) with
  advice, including idle Claude Code `bg-spare` workers.
- **Bounded state growth.** The job reaper no longer keeps receipts of jobs
  whose worktree was deleted (1103 of 1188 in production) unless a managed
  lease or workflow still needs them. Finished receipts of ordinary bounded
  operations older than 7 days are pruned in batches (7948 of 14715 on the
  production snapshot). Pending (UNKNOWN), deploy, release, integration,
  broker and admin receipts are kept.

## Performance

- One long-lived hardened `sqlite3` per state database (at most four per
  process) instead of one process per query, for the unprivileged daemon only;
  root broker processes keep the one-shot path so WAL side files never outlive
  an ownership change (`MCPSHKA_SQLITE_COPROCESS=0` restores it everywhere).
- Prefix reads use the primary-key index (21 ms -> <1 ms per query on the
  production snapshot); scans fetch up to 256 records / 2 MiB per round trip.
- `newServerWithCoordinator` (1553 lines) split into per-domain `tools_*.go`
  files; tool catalog byte-identical apart from the new fields above.

## Known limits

- The native integration worker cannot run: no production code issues the
  root integration approval (`ApproveIntegration` has no caller). Integration
  of PR #67 needs an explicit decision.
- Full `export` of 16k records through the helper takes ~5 min (fsync per
  record); acceptable for recovery, not for routine use.
