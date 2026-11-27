# MCPshka 0.1.63 autonomy candidate — HOLD

Checked 2026-10-08 11:12 UTC. Source runtime commit: 499c7f2.
Candidate: /root/repos/mcpshka/bin/mcpshka-0.1.63-autonomy.candidate
SHA-256: a7432cc4d80c00654cf4c136ed849ad03af47e222aa59c266904176e8fb608a1
Native ARM64 build from committed source; CLI reports mcpshka 0.1.63.

Checks: full go test -p 1 ./... and go vet -p 1 ./... passed.
After preserving legacy unknown evidence, internal/jobs tests and git diff --check passed.
Host-only adopted-child wait/reap regression passed; this is not Docker acceptance.

Changes: interrupted jobs retain unknown outcomes after restart; unknown jobs cannot be cancelled or reaped and reserve concurrency. Legacy interrupted-job evidence is retained. Existing project_lease_status exposes bounded sanitized project activity and explicit recovery blockers. No lease reclaim or owner-death inference was added.

Production remains 0.1.62, rollback 0.1.61. Fresh verify-production passed all runtime gates; aggregate failure is main dirty README.md and error.log, left unchanged. No deployment or service restart performed.

Release blocker: unknown jobs still lack trusted PID/start-time/boot identity and a bounded reconciliation path. Four unresolved jobs could hold the global concurrency limit indefinitely. Resolve and regression-test this before production deployment; diagnostics alone are not autonomous recovery.

RootLedger is healthy at PID 3856 with 42 Python zombies; one additional Codex zombie belongs to PID 2459, which also has active children. The proposed Compose init and exec-form healthcheck patch is not applied. A scoped guarded RootLedger recreate capability, isolated Docker regression and rollback/health acceptance are still required. Do not kill zombies or restart active Codex sessions.

Old main, DIAG and P7 leases remain untouched. Recovery requires trusted owner/session identity, PID/start-time and heartbeat proof, jobs/deploy reconciliation, writer fencing and CAS; absence of jobs or elapsed age does not authorize takeover. P4 and unverified iCloud uploader are excluded. DIAG deployment deferred to the process/autonomy priority.
