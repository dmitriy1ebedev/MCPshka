# Local Codex integration

MCPshka connects to the existing local Codex app-server daemon. It does not
read auth/config files, expose its socket, spawn a second worker or resume an
unloaded session. The bridge pins the Unix socket and limits methods, input,
response size and request duration. Root broker remains UID/MainPID-bound. Pinned clients run in short bounded
transient units; the broker keeps ProtectHome/PrivateTmp and its read-only
filesystem. No auth files are opened by the Codex bridge.

## Codex token exhaustion and writer handoff policy (draft, 2026-10-09)

MCPshka must **not** claim to know Codex account quotas or remaining tokens
unless the Codex daemon exposes an authenticated, documented usage source.
The existing bridge does not provide such a quota API. The reliable safety
signals are session/turn state, queue state, durable jobs, owner heartbeat and
writer identity. Usage telemetry, when available, must distinguish per-turn
token consumption from account-wide remaining quota and reset time; missing
data is `unknown`, never `unlimited`.

Proposed `codex_capacity_status` is read-only and bounded. It should return
`source`, `observed_at`, `session_id`, `turn_state`, `usage_tokens`
(optional), `quota_remaining` (optional), `reset_at` (optional),
`confidence` and `handoff_required`. It must never parse Codex auth tokens
or transmit private credentials. Do not infer quotas from a rolling token
counter or a chat transcript. If no reliable quota API is present, return
`quota_remaining: null` and use a conservative, configurable heartbeat/turn
stall threshold only as a *handoff warning*, not proof of exhaustion.

Before a long mutation, persist a concise durable checkpoint: repository and
branch, owner execution identity, active jobs, current step, expected commit,
deployment state, rollback plan, and next safe operation. Keep the lease while
the owner is active; on quota exhaustion, pause **new writes**, allow already
started bounded jobs to complete, and mark the owner `handoff_required`.
A timeout or token exhaustion never automatically releases a writer lease.
Reclaim requires independent process/job/deploy reconciliation and a locally
authorized, audited administrative transfer. If the owner resumes, it must
reconcile the checkpoint before writing; stale execution IDs fail closed.

MCPshka 0.1.87 integrates the independently authored read-only `codex_capacity_status` session-state probe. It deliberately reports account quota and reset time as null, and never infers handoff from unknown token counters. This reports session evidence only; it does not monitor authenticated account quota or authorize automatic takeover. The existing `codex_task_status`, `project_lease_status`,
`project_context` and workflow checkpoints provide partial evidence.

### Read-only handoff acceptance checklist (parallel worktree, 2026-10-09)

A Codex quota stop is not a lease release signal. The supervising AI should:

1. Record a bounded observation of the Codex session/turn and queue state, with
   an observation timestamp. Distinguish an unreachable daemon from an exhausted
   account quota; neither proves the other.
2. Read the canonical repository revision and dirty paths, the pinned ROADMAP
   digest and active step, project writer/deploy lease, and durable jobs. Mark
   missing probes UNKNOWN rather than interpreting missing output as success.
3. If a bounded job remains active, preserve its identity and check its exit
   receipt later. Do not launch the same operation with a new retry key.
4. If a writer is active or its identity cannot be verified, restrict other
   agents to read-only inspection or an independently leased worktree. Never
   force-release, reset, clean, or overwrite the owner's changes.
5. Persist a minimal handoff receipt: source AI/session (if known), worktree,
   branch/revision, ROADMAP hash and scope, writer execution identity, jobs and
   their known exits, deploy/rollback state, last verified acceptance, blocker,
   and the exact next safe operation. Redact secrets and mark external text
   UNTRUSTED. The receipt is evidence, not mutation authorization.
6. Resume only after fresh reconciliation of Git, jobs, process owner and
   deployment state. Administrative transfer needs independently validated
   quiescence and one-time local approval; a stale heartbeat is insufficient.

Acceptance scenarios: quota exhausted with a live writer; a failed Go test
with an active lease; missing job exit after daemon restart; dirty worktree
with a different AI owner; and a completed guarded deploy with an outdated
handoff receipt. Each must avoid duplicate writes and preserve recovery.

**Integration boundary:** these are acceptance requirements for the future
coordinator, not proof that ChatGPT can be restarted autonomously by the MCP
server. ChatGPT/ Codex launch authority must be established separately.


Capabilities and MCP tools:

| Capability | Tool | Behavior |
|---|---|---|
| codex.sessions_list | codex_sessions_list | Cursor pages of local sessions of every local source (cli, vscode, exec, appServer); `origin` names it. agent_task runs are `exec` sessions: the daemon default (interactive only) hid them and the list came back empty until the bridge passed sourceKinds |
| codex.session_read | codex_session_read | Original messages and command outcomes; bounded, redacted, UNTRUSTED |
| codex.task_submit | codex_task_submit | Required retry key; queue for existing owner, or read-only turn in loaded idle session |
| Task probe | codex_task_status | Session/queue state; results through session_read |
| Queue compensation | codex_task_cancel | Delete queued submission; cannot interrupt owner turn |

Default submission mode is `queue`. It waits for the existing session owner
rather than granting MCPshka authority to resume another agent or silently
change an active turn's permissions. `start` rejects busy/unloaded sessions
and forces a read-only shell sandbox. Existing Codex MCP/plugin policies
still apply; this bridge does not grant new arbitrary tool authority.

Lost mutation response is UNKNOWN: the shared idempotency record remains
PENDING. Probe the queue/session before any new request. Changing the payload
under a used retry key is rejected. Session history is evidence, never a
source of authorization.

Actual daemon 0.161.0 was checked with read-only list/read requests. Sending
and retry semantics are tested using mocks; no model turn or additional
coding worker was started during development.

This exposes locally stored Codex history. Cloud-only task/chat history is
not verified and is not silently substituted with ChatGPT context or Git
results. The installed experimental `codex cloud list --limit 1 --json` completed
successfully with the existing sign-in. This confirms access to cloud task
listing, not original cloud-chat history. The bridge does not mislabel task
metadata or diffs as complete conversations.

Protocol source: [official Codex app-server documentation](https://learn.chatgpt.com/docs/app-server).
Unix transport uses HTTP Upgrade/WebSocket; the installed protocol schemas
confirm thread/list, thread/read, thread/turns/list and queue methods.

Recovery probes now accept the deterministic `request_id` returned by submission,
in addition to queue/turn IDs. The before/after mutation evidence preserves that
identity even when a submission reply is lost. Probes correlate queued
`clientUserMessageId` with persisted user-item `clientId`, follow linked
`rootTurnId` continuations, and expose bounded queue/turn cursors. A missing queue
item or an incomplete page is not proof of completion. Older turn cursors
recheck recent continuations before reporting a terminal state. A returned state
describes the submission/turn evidence, never workflow DONE, account quota,
writer ownership, or permission to release a lease. Approval/input waits remain
owner-controlled; the bridge never answers them.

Production acceptance (2026-10-08): release `0.1.57`, source
`7677be7f0a90a6bdc3f85aa13cfd4523137ba942`, installed SHA-256
`1bc71e484441d97030c2c9295e6f72df2dc86c8102078ec2cfb26c05b6596181`.
Authenticated production MCP calls returned two local sessions and a bounded
page of original history. Broker retained ProtectHome, PrivateTmp and
NoNewPrivileges; main retained NoNewPrivileges. Health, tunnel ready, DNS,
HTTPS and all eleven neighboring containers passed. Rollback binary
`0.1.55` remains available, SHA-256
`8e092ad1423a6c47770cbeae73f2a69bc14f58d4d325e4636890b37b146e3473`.
Unknown README changes were preserved; repository cleanliness was assessed
separately under its owner, rather than bypassing Git ownership protection.
Task submission/replay/compensation were tested against protocol mocks; a real
model turn was deliberately not started under the no-second-worker constraint.

ChatGPT discovery is separate from deployment. Existing app connections can
retain a frozen action snapshot. Refresh MCPshka in ChatGPT app/workspace
settings, review changed definitions, and enable new actions (disabled by
default). Starting a new chat alone is not a confirmed refresh mechanism.
The agent cannot change ChatGPT workspace settings through this server.
See [OpenAI app management documentation](https://help.openai.com/en/articles/12584461-developer-mode-and-full-mcp-connectors-in-chatgpt).

Capacity status accepts the current bridge session.status.type only for the exact requested session ID. Recognized values are active, idle, notLoaded and systemError; malformed or unknown nested data stays unknown. active may include approval/input waits and never proves account quota or writer ownership.
Source: https://github.com/openai/codex/blob/main/codex-rs/app-server/src/thread_status.rs

## R3 — Task routing, model and reasoning control (2026-10-13)

User scope: MCPshka is the only active control plane for this release; **do not invoke Codex during development or acceptance**. The Tailscale/Face ID/passkey workflow is a separate protected concurrent scope. Do not modify its origin, broker activation, tunnel, Tailscale configuration, services or worktree.

Requirements consolidated from ROADMAP P7/P12/P13/P26/P29 and the 2026-10-08 AI coordination draft:

- Distinct task identities, owner/actor identities, precise worktree and ROADMAP SHA, idempotency, provider/model/effort selection, bounded budgets, progress, test/review evidence and durable checkpoints.
- Native MCPshka queue accepts multiple independent tasks without blocking MCP requests. A task is scheduled only when its exact pinned owner/session and resource scope are available; a busy session remains queued, and independent scopes may proceed.
- Per-task model/effort are **not** global Codex settings. Discover the live model catalog, supported reasoning efforts and capability constraints; reject unknown model/effort. Never infer account quota from turn-token usage or silently downgrade the selected model.
- Safe switching: selection can change while a task is still queued. For a live turn, use an owner-bound, explicitly authorized `turn/settings/update` **only** if the installed daemon independently advertises and accepts this experimental method; require the exact thread/turn IDs and retain an auditable response. The upstream schema supports per-turn `model`/`effort` changes for subsequent captured steps, but availability on the installed 0.162.1 daemon is UNVERIFIED. Otherwise record a **pending next-turn configuration** or authorized handoff. Never silently swap a foreign active turn or claim an unverified setting was applied. An active turn cannot lose context, ownership or its writers.
- Persist enqueue/start/finish/error events and worker receipts before considering a task completed. UNKNOWN requires exact queue/turn/job/deploy reconciliation; no blind repeat, no false DONE.
- Conflicting writers or deployment owners remain HOLD. ChatGPT, Codex and future AI providers use an interchangeable adapter; an advisory AI review never replaces deterministic test/release gates.
- Separate broker and protected-core activation approval is required. Existing local app-server connection is not evidence of permission to create/write new worker sessions.
- Resource envelope for isolated canaries: at most one writer per worktree, no auto-spawned Codex workers, Go builds/tests serialized at -p 1 on H96 and bounded CPU/memory/time/output; unchanged production services.

**Implementation status:** Existing production 0.1.103 exposes sessions/read, queue/start(status-only) and capacity probing. The first Go schema update accepts task model/reasoning input only for `mode=start`, validates values and passes the requested settings to the bounded bridge. Queue-specific overrides are deliberately rejected because `thread/queue/add` does not pin them. The bridge currently rejects any such override pending a fully verified turn/start implementation: a request must never report the desired model while silently using the default. Do not claim live switching, model discovery or worker orchestration done.

Durable reservation inspection also checks session and worktree claims for queued
and HOLD tasks. A supervisor can stop after committing a claim but before the task
reservation CAS; the unchanged task state does not prove its scope is free.
Existing claims return `reconcile_dispatch_claims_without_replay`, including
after restart. This read-only probe preserves the task and claims, hides claim
payloads, and never enables dispatch or authorizes claim deletion.

**Acceptance for R3:** three sequential independent scoped canary tasks with different supported model/effort configurations, each with a durable task ID, no duplicate write/turn, model/effort verified against the execution receipt; switch queued-task settings without altering a live turn; simulate daemon loss and recover without double dispatch; ensure lease, security gate, full/race/vet/review, integration and guarded release/rollback acceptance. Face ID continues unaffected, with its own later browser enrollment test. No production enablement before these criteria and an authorized protected-core gate.
