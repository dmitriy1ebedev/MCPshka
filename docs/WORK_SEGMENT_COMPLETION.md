# Voluntary work-segment completion

Voluntary work-segment completion before a final user report.

- `project_work_yield` closes MCP mutation admission and verifies idle project jobs, no UNKNOWN jobs, no workflow claim and no pending deployment.
- Requires the holder token, exact lease ID, generation, execution ID and Git revision. Only interactive tracked holders can finish this way; managed-job ownership remains on its recovery path.
- A bounded observed checkpoint and pinned holder deletion commit atomically in the existing SQLite records store. Journal collisions and stale pins change neither record.
- `project_work_yield_status` reads a historical immutable proof scoped to the caller. A later owner does not invalidate or get released by that proof.
- Cached clients can use `exec_start` with command `mcpshka`, args `["work-yield", JSON]`, and matching inner/outer retry keys. Stored job arguments contain the yield ID and a principal hash, never the holder token.
- Cancellation of a waiting exclusive request reopens admission. Read-only tools remain available; ordinary mutations on independent projects can run concurrently outside the brief finish section.

A final user report is an explicit client workflow boundary, not something the MCP server can infer from chat text. Progress messages do not release leases. Active/UNKNOWN operations retain HOLD. This is voluntary completion, not administrative transfer, process fencing, acceptance of a roadmap item, or permission to discard dirty work.

Before reporting a completed segment, the executor saves its detailed durable task checkpoint, calls work-yield and checks RELEASED. On a lost reply it reads the immutable yield proof before any retry. HOLD must be explained with the actual blocker; no automatic expiry or forced release is added.

Recovery with `mutation_probe` requires the exact original typed input. It checks both the outer receipt fingerprint and immutable journal input hash; a proof from a different native/compat request with the same retry key keeps UNKNOWN.
