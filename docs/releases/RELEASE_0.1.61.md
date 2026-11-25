# MCPshka 0.1.61 — tracked project lease heartbeat

Accepted 2026-10-08 (UTC 08:10). Deployment
`1791446847353-29933ddd5660` SUCCEEDED through detached guarded updater.

- Candidate source: branch `release/p7-lease-heartbeat-0.1.61`,
  cherry-pick of `c2370e7` as `b81d432`, version bump `8101ed1`
  over previously accepted 0.1.60 source `7c08945`.
- Installed `0.1.61`, SHA-256
  `34c1f41e9cf2e9533b38eba8618766ba76c43580f12272f108fb7f4801366e33`.
- Rollback `0.1.60`, SHA-256
  `b8d1b5b4d53dfc5b8c1d2dd7aa96ffd4563254b6f2ac50203d28df9389cb5a4f`.
- Source delta contains only lease core, MCP tool/contract/test and version,
  plus P7 lease docs. No root-broker/P4 rescue activation or iCloud P0 helper.
- Targeted tests for projectlease/mcpserver PASS. Full `go test -p 1 ./...`
  PASS. Git diff --check PASS; release source worktree clean before acceptance.
- Actual production smoke PASS: MCPshka service, tunnel, healthz and ready
  (v0.1.61), DNS, outbound HTTPS, SQLite, Home Assistant healthy and Docker
  metadata. Fresh real project lease showed `execution_id` and
  `heartbeat_at`, survived independent status probe, and was released.
- `project_verify.ok=false` only because unrelated prior changes
  `README.md` and `error.log` remain in main; all other checks were green.
  Do not reset, clean or overwrite these files.
- Existing main lease owner `chatgpt-tier1-p5-inspect-20261008-0918`
  and DIAG lease `diag-tier1-p01-20261008-0853` are legacy/untracked,
  not recovered or stolen. No auto takeover from missing heartbeat alone.
- New MCP tool `project_lease_heartbeat` is registered in the server but
  its client action schema is cached in the current chat; plugin refresh
  required for direct ChatGPT invocation. Production acquire/status contract
  of tracked leases was verified over live connector.
- P7 future slice: verified owner PID/start-time or remote authenticated
  execution liveness + bounded job/deploy reconciliation, CAS recovery gate,
  audit and forced-crash tests. Legacy leases require explicit trusted
  reconciliation; age alone is not proof of staleness.
- DIAG root-broker/iCloud P0 independent work is safely preserved on
  `work/diag-p0-broker-20261008`, commit `f1b6df4`. No verified iCloud
  upload bridge exists yet; local DIAG 0.1.1 capsule remains SEALED only.
