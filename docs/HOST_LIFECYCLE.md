# Host lifecycle and filesystem migration

The active H96 host is production. There is no general AI root shell. WebAuthn
may grant a bounded administrative *session* later, but identity verification
does not disable ownership, fencing, explicit approval, or rollback gates.

## Current state

- [x] Baseline read-only `host_storage_inventory` with bounded byte/entry counts,
      exact roles, and explicit UNKNOWN / partial evidence on restricted paths.
- [x] Conservative `host_housekeeping_plan`: KEEP or REVIEW only,
      zero promised reclaimed bytes until complete proofs exist.
- [x] Docker environment comparison is key/value based, not array-order based;
      conflicting duplicate keys are rejected.
- [ ] Privileged inventory helper: root-owned, narrow, read-only socket with
      fixed snapshots of Git/worktrees, open fds, processes, service units,
      Docker mounts, scheduled jobs, leases, hardlinks, package ownership and
      production/rollback binaries. Denials must return a reason and next step.
- [ ] `host_housekeeping_plan` must pin every proposed deletion to inode,
      device, metadata, content digest, explicit role, dependencies and
      reclaimable bytes; unknown means KEEP. Never delete state data or runtime.
- [ ] `host_housekeeping_apply/status`: durable transaction keyed to reviewed
      plan hash + owner, idempotency key, cancellation status and audit. Before
      each deletion recheck fingerprints, process and Docker mounts, leases,
      concurrent builds, source changes and immutable production pins. Any
      UNKNOWN yields HOLD and never retries destructive work blindly.
- [x] `host_relocate_plan` (read-only): validates canonical paths, rejects aliases,
      existing/nested destinations and pins inode/device, size and bounded SHA;
      unknown dependencies remain explicit HOLD.
- [ ] `host_relocate_apply/status`: enumerate systemd, Docker compose
      binds, symlinks, Python venv references, Git worktrees and installation
      scripts. Prove source and destination identities, preserve ownership and
      metadata. Journal transition stages, copy + fsync, revalidate, guarded
      cutover, service/mount/data/version verification, then automatic rollback
      on failed verification. Uncertain crash outcome must be reconciled by
      status from durable evidence before attempting another move.
- [ ] Error contracts: machine-readable `code`, `cause`,
      `allowed_parameters`, `safe_next_step`, `evidence` and `retryable`.
      Examples: root inaccessible -> broker inventory (no secret exposure);
      candidate cleanup HELD -> inspect active writer and pinned rollback;
      DIAG budget too low -> report minimum accepted RAM/CPU/wall time.

## 2026-10-16 integration checkpoint / release candidate

- [x] PR #13 merged to GitHub `main` at `d243e50c8c5f48812b6f1cf81c35a2d69a7277e3` after exact head `99e1d76dd2407b94a3078dafe1da25e1e4159a96` passed five cloud checks (release, race, fuzz, build, ARM64). Local `main` fast-forwarded to source tree `5f7880a1f8afeca897b2c79473cc09d3bbdbdbd6`.
- [ ] Versioned release `0.1.113`: pinned source, all five fresh cloud checks, verified ARM64 artifact, guarded deployment and production/rollback acceptance. This document's check is NOT release evidence.
- [ ] Restore independent Codex review via an authenticated loaded session. Previous local session is `notLoaded`; queued review rejected; executable absent in MCPshka job PATH. Do not claim Codex review completed or bypass another session owner.
- [ ] Fix interactive lease mutation admission: an owner-acquired interactive lease blocks its own `fs_patch` and `exec_start`; use a separately reviewed token-bound capability admitting only exact owner/generation, rejecting stale/foreign leases, before relying on this for autonomous edit pipelines. Existing managed-workflow lease guard is not to be weakened.
- [ ] P4 survivability: no independent verified OOB backup; do not run production network/tunnel fault injection. Work on isolated canary and safe read-only proof instead.

## Required acceptance

Simulated crash after every relocate phase; files in use, Docker bind mounts,
concurrent build and lease claim, modified source SHA, symlinks and external
mount boundary, stale replays, and key/value-equivalent Docker env permutations.
All negative proofs fail closed. H96 runs only bounded smoke; independent
cloud release/race/fuzz/build/ARM64 evidence is mandatory before guarded deploy.

**No deletion or relocation is authorized by the present read-only slice.**
