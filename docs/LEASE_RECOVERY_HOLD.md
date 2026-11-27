# Lease recovery: required evidence before activation

2026-10-08: production 0.1.62 provides tracked execution IDs, token-bound
heartbeats and SQLite compare-and-swap. It does not attest owner death.
Main and DIAG legacy leases report unverified owners and missing heartbeat.
Their age cannot authorize recovery. Execution ID is not an OS PID identity.

The recovery capability must use server-verified evidence, never accept
caller assertions that an owner is dead. Enrollment must bind owner session,
execution ID, host boot ID, PID and /proc start-time. Remote owners additionally
need authoritative session liveness; a disconnected transport is insufficient.

Bounded read-only recovery_probe must snapshot the exact lease fingerprint,
heartbeat and bounded owner/job journal; verify PID start-time and owner session;
reject inaccessible evidence, unknown/running owner jobs and active deployments.
Legacy leases without enrollment remain HOLD, requiring owner handoff or an
independently verified recovery procedure. Do not retroactively bind an old
lease to a convenient PID or use an empty jobs list as proof of owner death.

Recovery apply must reverify evidence under serialization and compare-and-swap
the entire previous lease into a new lease with a new secret token. Record a
durable sanitized recovery journal. A concurrent heartbeat, release, new job
or PID reuse invalidates the probe; failed CAS changes nothing. Do not delete
rows manually, infer death from timeout, or weaken broker/auth policy.

Required regression gates: live owner; PID reuse; boot change; inaccessible
/proc; remote owner unknown; running/UNKNOWN job; deployment active; heartbeat
race; CAS conflict; restart before/after apply; duplicate idempotency request;
legacy untracked lease. Activation is HOLD until this enrollment/verifier path
is implemented and tested. No recovery mutation was attempted in this audit.


## Root-sealed administrative exception (0.1.133 candidate)

This is a separate, root-authorized maintenance transfer for an unbound,
execution-tracked SQLite legacy lease, not automatic remote owner-death
inference. The operator must keep both the project root and common Git
authority root-owned at mode 0500 and maintain a closed SQLite writer gate.
An active authenticated Face ID administrative session is checked by the root
broker; a read-only preview pins owner, execution ID, record SHA, committed
Git tree and immutable journal ID. Apply rechecks process references, Git
identity, tree and worktree, rejects active/unknown deployments and does a
single SQLite compare-and-swap of generation, token and an audit receipt.
The old token cannot release the successor. An unbound record without a
tracked execution ID, a dirty tree, missing process evidence, and a writer
without enforceable root sealing remain HOLD.

**Operational boundary:** the admin tool does not create the OS seal or
grant root permission to the MCP daemon. A trusted root operator must arrange
the seal independently and retain it through integration, successor claim and
explicit gate opening. The root broker binary must first be updated through
the protected broker installation workflow. An UNKNOWN result is inspected,
not retried. The normal preferred release is still a token-bound
`project_lease_release` executed by the original holder.
