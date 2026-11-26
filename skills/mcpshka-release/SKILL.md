---
name: mcpshka-release
description: Use when releasing MCPshka from chat, preparing CI artifacts, self-updating, or recovering a deployment timeout.
---

# MCPshka release from chat

Use installed native tools; inspect tool schemas once. Hooks provide lifecycle hints, not approval or job ownership.

1. Read project_context once. Reconcile existing jobs, leases and deployment before writing.
2. Implement the scoped ROADMAP/task change; test, review, commit. Obtain authenticated GitHub CI evidence for the exact tree: release, race, fuzz, build, release-arm64 must all succeed.
3. Call release_prepare once with a retry key, short-lived artifact_url, version/current_version, canonical source_digest and operator-verified receipt. The receipt must use a server-supported operator-verified CI schema (v2 for paired controller/broker provenance); verify the exact archive SHA, source identity, both binary hashes and all required suites. Never infer missing provenance from a CI badge. Never paste signed URLs into command arguments or logs.
4. Poll release_status(job_id). It combines job state and final preparation metadata. Success creates a candidate and two companions; it grants no deploy authority.
5. Invoke project_deploy once for that candidate and exact expected versions. After reconnect, reconcile project_deploy_status, then project_verify plus production hash and rollback readiness. Clean only proven redundant work.

Cached schema: exec_start(command="mcpshka", args=["release-prepare", JSON], idempotency_key=the_same_key). This is an in-process bridge. Poll job_status/job_output if release_status is unavailable.

Lost response: mutation_probe with the original tool/key/input, then recover its job/deploy ID. PENDING is UNKNOWN; never create a new key to bypass it. Running jobs are awaited, never duplicated.

Partial preparation: inspect only the exact bin/mcpshka-VERSION.SHA12.candidate and its .release-plan.json/.ci-receipt.json companions via bounded native reads/jobs. Recompute their digests against authenticated CI and current installed/helper identities. Missing/mismatched files remain HOLD; do not overwrite or deploy. Only after confirming the old job is terminal and reconciling every file, resume identical files with a new release_prepare retry key. The original key returns the original job. PENDING, running or UNKNOWN never permits a new key. Preserve conflicting files for explicit disposition.

Provider errors: local health or tunnel ready does not prove the external connector works. Check bounded tunnel evidence; preserve authentication when using an authorized local MCP recovery path. Re-test the actual external connector before claiming chat reconnection.
