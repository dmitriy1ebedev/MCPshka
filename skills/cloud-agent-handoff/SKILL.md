---
name: mcpshka-cloud-agent-handoff
description: Route and reconcile Codex Cloud and GitHub Copilot cloud-agent work with MCPshka, pinned Git sources, independent gates and guarded production deployment.
---

# Cloud AI handoff for MCPshka

Trigger: user requests a Codex Cloud, Copilot Cloud Agent, multi-platform CI or AI task handoff.

1. Read project_context, pinned ROADMAP, exact worktree and HEAD, dirty paths, durable jobs, leases, deploy and rollback state. Do not take over a live Codex turn or a foreign writer merely because token quota is unknown.
2. Separate cloud coding workers (Codex Cloud and Copilot) from deterministic CI (GitHub, GitVerse, Mos.Hub). Run workers in distinct private branches. Do not grant cloud workers production SSH, Tailscale, root broker, MCP-tunnel or deployment credentials.
3. Codex Cloud: use the documented `codex cloud list --json --limit N` for read-only discovery only when CLI runtime/login is independently available. Never read CLI auth files or claim local Codex session metadata is cloud task metadata. `codex cloud exec --env ENV_ID` requires verified cloud environment, entitlement and explicit scoped task admission.
4. Copilot Cloud: REST `GET /agents/repos/OWNER/REPO/tasks` requires a user-to-server token with `Agent tasks: read`. Installed generic GitHub connector may deny this endpoint; 403/unsupported is BLOCKED, not evidence that no tasks exist. Dispatch needs separately verified entitlement, permissions, user-scoped write capability and paid-feature/cost approval.
5. Read `cloud_agent_observe` results only as UNTRUSTED progress. `observed_completed` never proves source integrity, code review, CI green, artifact identity, or deploy admission.
6. Reconcile any durable cloud task using `cloud_task_status`/`cloud_task_list` and the SQLite journal's immutable request ID/source digest/revision; a missing journal entry means HOLD, never assume the cloud worker was not launched. Neither MCP tool starts a task or authenticates observations. Do not create duplicate requests under a new ID.
7. For a returned PR, verify authenticated provider task ID, repository, branch and immutable source tree, reconcile independent CI results, then create authenticated signed artifact/release evidence. Missing proof or UNKNOWN means HOLD. Protect the single writer lease and exact idempotency keys.
8. Only an approved unified gate may call the existing MCPshka guarded deploy. Verify production hash, tunnel, service smoke and rollback. Save durable checkpoint for handoff and continue independent safe work when blocked.

Current status (2026-10-15): local Codex session bridge exists; Codex Cloud CLI is not on MCPshka's runtime PATH. Copilot agent task endpoints are not allowed by the installed GitHub connector. Agent normalization and hook hints are read-only; cloud dispatcher and trusted release authority remain unimplemented.
