# Mutation contracts

Registration rejects an unknown mutation, missing retry-key input, missing
read-only probe, absent compensation tool or unbounded contract. New tools
must add a reviewed contract; read-only status calls never perform recovery.
`mutation_contracts` exposes the compact registry without source-file reads.

Each mutation requires a retry key, or a validated natural key for cancellation
of queued tasks and jobs. Restore verification requires its own attempt key so a known failed isolated check can be retried intentionally. Optional legacy retry keys are now required. Refresh
client action schemas before using this release. Authentication, scopes,
protected-core policy and PID-bound broker checks still apply.

The registry persists a separate `contract:` receipt in the existing shared
idempotency store. It captures bounded live before/after probes, fingerprints
and short state fields, without file contents, logs, messages or secrets. A
completed handler receipt is replayed without repeating the handler or probes.
Async acceptance is not job/deployment/backup completion: inspect the referenced
status tool. `mutation_probe` reports receipt status and optionally probes the
live resource. It never grants retry authority or clears UNKNOWN.

A missing response, failed after-probe, oversized response, process death or
failed receipt write leaves PENDING/UNKNOWN. Reconnect and probe before deciding
on recovery; never send a new key merely to bypass a held request. Result
payloads remain in the private state database and are not returned by probes.
Before/after audit records use hashes and compact state only.

Compensation is explicit and resource-specific; nothing performs blind undo:

| Resource | Probe | Compensation |
|---|---|---|
| Staged LANE network | network_stage_status | network_stage_rollback; external dead-man stays authoritative |
| Systemd/Docker lifecycle | service_status / docker_status | inverse start/stop via action tool with a new reviewed key; restart cannot undo elapsed interruption |
| Exact file patch | fs_read | inverse old/new replacement, guarded by the observed current SHA-256 |
| Writer lease | project_lease_status | token-bound release; reacquisition must check the current owner |
| Managed job | job_status | job_cancel stops future execution; it cannot undo arbitrary completed command side effects |
| Guarded deployment | project_deploy_status | guarded deployment of retained previous release; helper automatically restores on failed acceptance |
| Codex queue | codex_task_status | codex_task_cancel deletes only this queued submission; completed work stays evidence |
| Approval / DIAG observation | approval_status / traffic_route_status | server-enforced TTL / bounded observation lifetime |
| Isolated restore check | state_backup_status | repeat verification in a fresh scratch destination; no live overwrite |

Broker installation, service-user migration, destructive candidate cleanup and
creation of an off-node backup are explicitly irreversible through these tools.
They require `mutation_approval_request`, followed by a trusted local grant using
the existing administrative approval CLI. The token binds the exact normalized
tool payload and retry key, is consumed once and is excluded from replay
fingerprints. The MCP request tool cannot grant its own approval. Candidate
cleanup planning remains read-only in effect; applying the plan requires the
grant. Underlying policy/destructive-exec approvals remain mandatory too.

Receipt completion means the handler completed and both probes were captured;
it does not assert reversal of arbitrary external effects. Generic exec and
project jobs retain their command policy and destructive-operation gate. The
job contract covers durable launch/cancellation, not transactional shell effects.

### Candidate cleanup snapshot boundary

`project_cleanup_candidates` selects direct `bin/mcpshka-*.candidate` files and
`bin/candidate-state-*` entries. Other build assets are outside its scope and
remain untouched. Production and rollback copies are recognized by actual
SHA-256 content, including aliases with version suffixes or misleading names.

The current implementation only snapshots and preserves artifacts. Unverified
ownership, symlinks, hardlinks and state directories remain UNKNOWN and applying
a plan returns HOLD. Snapshot reads reject nonregular descriptors without
blocking on FIFOs, enforce entry/content limits and revalidate pinned descriptors,
public paths and mount identities. A successful apply is a verified no-op;
`remove` stays empty. No deletion executor or ownership/fencing proof exists yet.
Sequential snapshot checks do not establish writer exclusion, and protected
version commands separately reopen their paths. Destructive cleanup requires a
sealed ownership boundary and durable intent/recovery receipts before activation.

`self_heal_status` is a read-only SQLite probe with normal reader/scope checks;
it has no mutation replay key and never invokes Docker. The autonomous P15 loop
reserves each restart in SQLite before dispatch, audits the transition, and
verifies health in later passes. A lost reply resumes verification only; failure
escalates without another mutation. See [Docker access](DOCKER_ACCESS.md#bounded-container-self-healing-p15-first-slice).
