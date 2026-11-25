# Cloud AI workers for MCPshka: explicit trust and deployment boundaries

MCPshka remains the only production authority. GitHub Actions, GitVerse CI and
Mos.Hub can execute independent code-quality checks on a pinned source. Codex
Cloud and GitHub Copilot cloud agent are **coding workers**, not substitutes for
independent CI gates. Their proposed patches stay on review branches and must
be re-validated by MCPshka before merge or guarded production deployment.

## Provider integration status (15 October 2026)

Historical status below. For the 24 October C1 implementation, installed CLI
probes, Face ID dispatcher, current limitations and owner setup, see
[CLOUD_AGENTS.md](CLOUD_AGENTS.md).

| Worker | Capability and verified route | Outstanding block |
| --- | --- | --- |
| Local Codex | Existing MCPshka `codex_sessions_list`, `codex_session_read`, queue/status/cancel; bounded local daemon bridge | Session notLoaded is not a quota exhaustion proof; R3 durable dispatch/model/effort admission not DONE |
| Codex Cloud | Official CLI `codex cloud list --json --limit 1..20`, `codex cloud exec --env ENV_ID QUERY`; existing login had worked in a prior isolated check | `codex` executable not on current MCPshka job PATH; cloud task provider identity and safe worker authority not verified |
| Copilot Cloud | GitHub agent-task REST API has task list/get/start endpoints; repo-scoped user access token with Agent tasks permission required | The installed GitHub connector blocks `/agents/...` APIs; user-token permission, paid eligibility and repo policy not verified |
| GitVerse | Private snapshot mirror plus existing cloud CI | Recent write rejected; new dispatch and quotas not verified |
| Mos.Hub | Possible additional independent scans/builds | Authentication, runner/API capabilities and quotas not verified |

**Cost gate:** do not assume Copilot cloud-agent entitlement or upgrade. The
published GitHub documentation says Copilot cloud agent requires a paid plan;
there are plan-specific limitations on starting tasks over REST. User-to-server
tokens, not GitHub App installation tokens, are required for agent task APIs.
Do not copy account secrets into any worker repository, MCP receipts or H96 logs.

## Normalized read-only task observation

`python3 packaging/cloud/agent_tasks.py --provider codex_cloud --task-id TASK_ID --input codex_tasks.json`

`python3 packaging/cloud/agent_tasks.py --provider copilot_cloud --task-id TASK_ID --input copilot_tasks.json`

The input is a bounded, externally obtained JSON response, not a trusted
authenticated provider result. Codex `tasks[]` items use `status`; GitHub's
agent-task `tasks[]` items use `state`. Only task identity and status are copied
into the output, never prompts, URLs, summaries, secrets or patch contents.
Output explicitly says `UNTRUSTED`, `source_verified:false`,
`artifact_verified:false` and `deploy_authorized:false`, even after
an observed completed task. Missing/pending/unknown are HOLD. Duplicate task
IDs, malformed JSON, oversized files and symlinks fail closed. The CLI does not
open network connections, invoke Codex, start Copilot or grant worker leases.

Before making a real cloud-agent dispatcher, add bounded provider-authenticated
adapters with token isolation, idempotent tasks, cancellation/status/pagination,
and exact source-digest/branch PR handoff. Bind the task to a verified repo,
worktree, source SHA and owner identity; verify returned commits and reviews,
then run all independent release gates. A provider's completed status is
never itself release approval. Separate AI worker reservations from CI jobs and
one-writer project leases.

## Sane division of work

- GitHub: full RELEASE, race/fuzz, real Linux ARM64 CGO build, artifact digest.
- GitVerse: independent architecture checks and compatibility when provider CI is reachable.
- Mos.Hub: SAST/SCA and independent supply-chain checks, only if authenticated runners satisfy policy.
- Codex Cloud: short bounded engineering tasks on isolated GitHub branches.
- Copilot Cloud: optional parallel review or independent implementation on a separate PR, only with verified entitlement; avoid multiple writers on the same worktree/branch.
- H96: source/artifact provenance, policy, signing verification, guarded deploy, production smoke and rollback.

Durable cloud task journal (see `docs/CLOUD_TASK_JOURNAL.md`) now provides
SQLite-backed, idempotent staging and CAS-protected observations in the Go
service layer, plus read-only `cloud_task_status` and `cloud_task_list` tools.
Its writers are not exposed to cloud agents until an authenticated provider
adapter and protected mutation admission have been reviewed. It does not dispatch
workers or authorize production.

Next deliverables: authenticated provider API adapters and admissible durable
dispatch/reconciliation; unified trusted release manifest and build provenance;
automated download and guarded deployment of the *same tested ARM64 binary*.
No ROADMAP DONE mark until these are operational and accepted.
