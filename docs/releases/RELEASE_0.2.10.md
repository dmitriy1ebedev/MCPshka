# MCPshka 0.2.10 - honest executor readiness and bounded stop/reconcile

Date: 2026-11-01. Source: issue registry r20 (docs/ISSUE_REGISTRY_2026-11-01_r20.md).

## Fixes
- Executor readiness (MCP-002/045/046/051): root health and the stale-permit preview read `executor/permits/` in one paged SQLite scan. Root has no SQLite coprocess, so the old per-record read started one `sqlite3` per permit; 97 records missed the broker's 4 s budget at load 12 and the executor stayed UNKNOWN. Semantics, the 512-record window and fail-closed UNKNOWN are unchanged.
- jobs_workflow (MCP-011/049): the ROADMAP 90% headroom warning stays visible in health data but no longer degrades `jobs_workflow`, which blocked `exec_start`, `project_run/test` and `roadmap_run` with no job fault. Only a roadmap over the hard limit degrades it.
- `agent_task_cancel` (MCP-053/055/100): owner stop of a delegated task under Face ID. Root stops the runner and attempt units; the record becomes `cancelled` only after both are observed ended. A failed stop or probe returns UNKNOWN and leaves the record unchanged. Repository work is kept; a cancelled task can be continued explicitly with `resume_of`.
- `approved_task` (MCP-028/092): plans whose write paths do not exist are refused at prepare and again before the grant is consumed. systemd cannot sandbox a missing `ReadWritePaths` entry and failed at NAMESPACE (status 226) before the worker ran (task e3af7683, 01.11). New workers record their start before the command, so a vanished unit without that mark reports effect `none`; legacy records stay `unknown`.
- Network diagnosis (MCP-097): `service_status` reads status (never logs or actions) of the fixed VPN/LANE unit families `openvpn*`, `wg-quick@*`, `xray*`, `lane-*`, `laned`, `tailscaled`.
- `job_wait` caps one wait at 45 s; MCP clients abort calls at about 60 s and lost the answer.

## Checks
- Targeted package tests: statestore, executor, agents, rootbroker, readiness, toolcontract, systemdsvc, mcpserver.

## Release gates
- Exact-commit CI, protected integration, coordinated installation, production verification and rollback readiness are required; this document is not evidence that they passed.

## Rollback
- The previous installed controller/broker pair is 0.2.9.
