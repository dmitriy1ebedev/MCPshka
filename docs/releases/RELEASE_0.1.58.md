# Production acceptance: 0.1.58 / P3

Accepted 2026-10-08 from committed source
`f82bfe9544b4f3c975a6ff133ce29a2e14450e55` (`git archive`, one bounded build).
Installed SHA-256:
`1a5ca626bd8e4740340610ccd688ce72eec3687bed739996c968114b87bb105f`.
Guarded deployment `1791434737710-fc210673d9a2` completed successfully.
Rollback is `0.1.57`, SHA-256:
`1bc71e484441d97030c2c9295e6f72df2dc86c8102078ec2cfb26c05b6596181`.

P3's six stages are accepted:

1. Jobs, approvals, leases, idempotency and deployments use the shared SQLite
   WAL record authority. Artifact bytes/audit/configs remain files; there is no
   fallback to stale legacy records while the SQLite marker is active.
2. A full production copy (2337 records / 2114988 payload bytes) passed schema
   migration 1→2→1, fresh restore and latest-record legacy export byte-for-byte.
   The failed 0.1.56 deployment exercised real guarded legacy rollback: latest
   SQLite state was exported before 0.1.55 resumed. Tunnel restart was fixed
   before the successful 0.1.57 and 0.1.58 deployments.
3. Full encrypted off-node backup `de1364a44cc5504c58614d1d016f018c`
   passed upload, download, decryption and fresh isolated restoration of 2290
   records. Ciphertext SHA-256:
   `d554ccfbe5e80744fac6e19ac68c6ded3c8e20c24be85e24fc18cc1611300756`.
   An additional production MCP restore attempt succeeded at
   `2026-10-08T04:47:31Z`; receipt `restore_state=verified`, scratch empty.
   A prior repeat encountered HTTP 403; no duplicate upload was performed.
   HTTP status/reason diagnostics are bounded and omit credentials.
4. All 26 mutations have mandatory registration contracts and real bounded
   before/after probes. Unknown tools/missing keys/probes/recovery/output caps
   fail closed. Explicitly irreversible tools require an exact one-time
   administrative grant. See TOOL_CONTRACTS.md for compensation boundaries.
5. Full Go suite/vet passed, followed by final affected-package suite/vet.
   Fault tests cover interrupted SQLite writes, corruption, unsupported
   schema/symlinks, migration/export; contract tests cover missing contracts,
   replay/conflict, lost after-probe and one-time approval. Native crypto/restore
   tests also cover safe HTTP failure receipts without credential disclosure.
6. Committed candidate passed an isolated migrated-production-copy smoke.
   Production reconnect and authenticated MCP calls passed: contracts registry,
   real read-only job/replay (same job `muz20i6j-1`, no duplicate effect), live
   mutation probe, local Codex list and backup restore. Installed identity/hash,
   health/ready, tunnel, DNS/HTTPS and all eleven neighboring containers passed.
   Main/broker NoNewPrivileges and PID-bound auth were preserved.

Unknown README changes and an untracked error.log were preserved. Git ownership
and this known dirty-tree exception were checked separately; they were not
silenced by global Git configuration. No push or second coding worker occurred.
Cloud-only Codex conversation history and independent OOB management paths are
not claimed as accepted by P3. P0 VPS acceptance remains deferred.
