# P7 — tracked project lease heartbeat (2026-10-08)

This slice is backward-compatible with older lease records and deliberately
does not include automatic takeover.

- New MCP project_lease_acquire obtains a stable, bounded 32-hex execution ID
  derived from the caller's idempotency key and stores it in the SQLite lease.
- A tracked record contains its acquired time and latest heartbeat. The new
  project_lease_heartbeat requires the holder's secret token and a new
  idempotency key for each renewal. SQLite CAS prevents renewing an already
  replaced or deleted lease.
- The tool's audit event intentionally excludes the token; status exposes the
  execution ID and heartbeat timestamp only.
- Existing leases with missing execution identity remain HELD and cannot
  automatically enroll in tracked recovery. The file-store fallback also
  refuses heartbeat because it lacks the SQLite CAS transaction.
- No stale lease is automatically reclaimed by this change. Recovery still
  needs verified owner liveness (PID plus start time for local jobs, or
  authenticated remote execution heartbeat), fenced jobs/deploys and an
  atomic compare-and-swap recovery mutation.
- For remote ChatGPT/Codex turns, a request-derived execution ID does NOT
  prove a host PID or process liveness. Never infer that a stopped heartbeat
  alone means the writer is gone.

Acceptance: cross-instance SQLite persistence, token rejection, no duplicate
writer, no resurrection after release, missing metadata fail closed, sequential
monotonic heartbeats, and generic MCP tool registry/conformance.

Current prod 0.1.60 is unmodified until an isolated candidate is built,
preflighted and guarded deploy accepted. The existing main and DIAG leases
have no owner liveness metadata and remain unresolved.
