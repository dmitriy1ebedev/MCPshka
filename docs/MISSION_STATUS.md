# Mission progress status

`mission_status` adds an on-demand read-only progress projection over bounded
agent task and roadmap journals, canonical SQLite integration queues and writer
leases. Each item reports `current_phase`, `evidence_age` in seconds (null for
missing/future evidence), `blocked_by`, `next_check`, and a read-only
`next_safe_action`. The report includes advisory `independent_work_available`
(null on incomplete discovery) and up to eight free immediate `/root/repos`
projects/worktrees. This is discovery, not permission to write: acquire a lease.

Evidence older than 30 minutes in an active lease/task/run, or a running record
whose unit is inactive/failed, produces `degraded`. An active unit alone never
refreshes evidence. Only an exactly matching `roadmap-run:<id>` lease can refresh
a roadmap run's evidence. Agent journals currently have no periodic heartbeat;
its start/end timestamps describe the latest recorded progress, so a long live
turn may require inspection even when it is healthy. A stale signal never
releases a lease, starts another writer or suggests replaying a task.

Blocker classes (lease, quota, Face ID, CI, worker) come from recorded evidence.
Queue v1 has no timestamps or CI/approval proof: missing proof stays unknown,
not an invented stage or age. A held project lease is an explicit queue blocker.
Terminal task/run blockers disappear when superseded by a newer completed run.
No prompts, output tails, lease tokens or raw errors are returned.

Collection is bounded to 1.5 seconds, 128 directory entries per fixed source,
32 SQLite keys per namespace and 16 output items; overflow/errors remain
explicit as incomplete (`truncated`, `unknown`, unless a known blocker wins).
`health_snapshot` includes a compact `mission` summary and a `mission_status`
follow-up action. LANE/index degradation still preserves healthy tunnel/Docker
observations. No background monitor, unit restart or ownership mutation is added.
