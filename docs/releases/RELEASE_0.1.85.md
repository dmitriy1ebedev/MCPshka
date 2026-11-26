# MCPshka 0.1.85 — pinned lease handoff CAS

Leases expose a non-secret lease ID and generation. New acquisitions start at
generation 1; release/reacquire creates a different lease ID even when owner and
execution ID are reused. Legacy records expose a stable derived ID and generation
0 without rewriting their authority. Heartbeats preserve identity.

The internal administrative transfer primitive pins owner, execution ID, lease ID
and generation before invoking its private fence. Ownership CAS now runs inside
the controller's critical section, not after it. A successful handoff retains the
lease ID, advances generation and replaces holder credentials. Concurrent heartbeat
or replacement causes CAS refusal. Generation overflow fails closed. A failure
after committed CAS is explicitly UNKNOWN and requires reconciliation.

Regression tests cover wrong owner before fencing, ABA reacquire, generation
mismatch, unbound owner, CAS within fencing, old credential rejection, heartbeat
race and post-commit UNKNOWN. Full-suite/vet and production acceptance are recorded
in the private checkpoint only after those checks actually complete.

No production transfer endpoint or trusted fence controller is installed yet.
Caller-supplied idleness booleans cannot authorize a transfer. Legacy interactive
leases, including DIAG P02, remain HOLD until authenticated actor fencing and
deployment reconciliation are enforced. This release is a prerequisite, not
completion of the universal Lease Reconciliation Engine. Protected core unchanged.
