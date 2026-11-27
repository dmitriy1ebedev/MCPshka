# Managed bounded workflows

## Historical deployment receipts

`project_deploy_reconcile` and `project_context` retain fenced nonterminal
receipts as audit evidence. `nonterminal_count` includes those historical
unknown outcomes; `fenced_count` identifies receipts independently retired by
the root-owned fence. The aggregate `reconciliation_required` describes only
active blocking receipts. A historical receipt still reports its original
state and its own `reconciliation_required`, because its past outcome has not
been established. It must never be replayed or relabeled as a successful deploy.
Missing or invalid fence evidence continues to return HOLD.

## Reproducible Go test storage

Native `project_test` Go jobs and ordinary built-in MCPshka `project_run`
workflows use job-owned `PrivateTemp` storage and `GOMAXPROCS=2`. Managed
workflow execution keeps `GOMAXPROCS=1` and its stricter resource limits.
Temporary files remain
inside the durable job directory; the existing job reaper owns their lifecycle.
The job-state filesystem must satisfy the disk reserve; moving temporary
files does not waive admission or fencing.

On H96, `/tmp` is a 1.9 GiB tmpfs and cannot satisfy the mandatory 3 GiB reserve.
Artifact upload fixtures and protected executor import/ref fixtures therefore
fail there by design. Direct bounded `exec_start` Go tests must explicitly use
`TMPDIR=/var/tmp`, `GOMAXPROCS=2` and `-p=1`, or use the native project runner.
Keep test output, terminal job state and exit code as evidence. A timeout or
HOLD is not a successful test. Run targeted, full, race and vet checks separately
within host budgets; production reserve/security checks must remain enabled.

## Administrative writer transfer — development checkpoint (2026-10-09)

Branch `chatgpt/admin-lease-transfer-20261009` in isolated worktree
`bin/p7-lease-freshness-wt`. The read-only MCP tool
`project_lease_admin_review` reports owner binding and fail-closed transfer
requirements. `projectlease.TransferAdministrative` is a SQLite CAS primitive
that replaces the writer token and fences the old owner; it requires an explicit
proof and is **not** exposed as a remotely callable mutation. Regression tests
verify missing proof and stale-owner rejection. Full tests, vet and build passed
on this branch as of this checkpoint.

**Not DONE:** a trusted administrative controller must independently verify
process/job/deployment quiescence, bind a one-time locally granted approval to
an exact owner/execution/repository identity, record audit evidence before
transfer, and expose a bounded, idempotent MCP administrative operation. Do not
accept caller-supplied booleans as proof. Do not merge into the main branch or
deploy while its Codex writer lease is held. After integration, repeat tests,
review, guarded deploy, reconnect and production verification. The main lease
was held by `codex-roadmap-p7-p10-20261008` with unverified owner identity.


## Read-only project handoff (0.1.81)

`project_context` takes `path` and optional `expected_roadmap_sha256` and returns
at most 32 KiB of UNTRUSTED evidence: canonical Git root/revision, dirty status,
ROADMAP/current item, durable claim, writer lease, jobs, pending deployment and
available test/build/deploy/verify profiles. A nested project cannot hide the
parent worktree claim. Missing capabilities produce warnings; a copied module
does not claim the host deployment capability. Metadata reads are bounded and
reject FIFO/symlink inputs without blocking. No ownership is transferred.

Cached clients use `exec_start` with command `mcpshka`, args
`["project-context",PATH]` or `["project-context",PATH,ROADMAP_SHA]`, a fresh
observation key and then `job_output`. This receipt is evidence, not permission
to replay an UNKNOWN mutation or destroy another session's work.

## Durable profile pipelines (0.1.80)

A trusted project profile may define `pipelines.release` with `stages` equal to
`inspect, modify, test, review, commit, build, deploy, reconnect, verify, cleanup`
and separate `rollback` and `finalizer` workflow names. Each name references an
existing validated command workflow. No commands are taken from evidence or logs.
Profiles retain their 64 KiB cap; all resolved pipeline plans together fit 32 KiB.

`workflow_start` takes `id`, `path`, `pipeline`, exact `roadmap_sha256`, ROADMAP
phase `scope` and `idempotency_key`. Admission requires a clean canonical Git
worktree, no active project job, no existing lease and no pending deployment.
Pins, inspection and durable worktree claim share the native admission lock.
Ordinary jobs, leases and fs_patch cannot bypass the claim. `workflow_status`
returns durable progress; `workflow_resume` reconciles HOLD without replaying a
reserved mutation. Background maintenance advances a rotating bounded page of
ten claims every ten seconds, independently of the client connection.

Cached clients use `exec_start` with `command=mcpshka`, a retry key and args
`["pipeline","start",ID,PATH,PIPELINE,ROADMAP_SHA,SCOPE]`, or
`["pipeline","status",ID]` / `["pipeline","resume",ID]`. Read the returned job
receipt using `job_output`; its completion describes the control request, not
completion of the pipeline. Use a fresh key for a fresh status observation.

Each stage has a deterministic job ID and durable admission before spawn. A
known failed exit invokes rollback then finalizer once. After restart, a lost
exit remains UNKNOWN; verified process-group/scope disappearance permits the
trusted compensation chain, never fabrication of success or stage replay.
Live descendants, unavailable authority, changed pins and uncertain recovery
outcomes retain ownership. In 0.1.82, a fixed isolated pipe gate waits until the
job receipt and immutable process-owner proof are durable before executing the
command. Pre-permit supervisor death kills the gate in the kernel. A missing
mutable receipt is reconciled using the immutable proof and fresh resource
checks; an absent proof is reread after the supervisor-death fence. Verified
resource disappearance advances rollback/finalizer without replaying the stage.
Legacy launches without this protocol still retain HOLD; absence of an old
record does not prove absence of an effect. Unknown compensation outcomes also
retain HOLD rather than inventing rollback success.
Scope disappearance requires fresh bounded systemd and kernel cgroup evidence.

Stage zero exit is the profile's declared postcondition; semantic assertions
belong in its test/review/verify workflows. The isolated semantic canary changes,
tests, commits, builds, installs, verifies and cleans a private artifact; it does
not deploy the host daemon. Host self-deployment remains owned by the guarded
updater. Automatic project profile selection and general AI-handoff acceptance
are separate from this executor; P7 is not declared fully DONE by these tests.

MCPshka 0.1.64 adds an opt-in job-owned writer lease. Existing interactive leases remain explicit-holder leases; they never expire solely by age.

For clients with cached MCP tool schemas, use `exec_start`:

```json
{"command":"mcpshka","args":["project-task","/path/to/project","test"],"workdir":"/path/to/project","idempotency_key":"unique-task-key"}
```

The path must satisfy the existing policy and the named workflow must resolve from the existing project profile. No shell, approval or environment overrides are accepted. This returns the actual bounded job, not an acceptance-only wrapper. New clients may also call `project_run` with `managed: true`.

The control plane atomically acquires a declared deterministic owner identity before starting the job. The private holder token stays in authority, never in argv or job output. Managed tasks are capped at 512 MiB, 32 processes, 300 seconds and 16 KiB output; Go uses GOMAXPROCS=1/GOMEMLIMIT=384MiB. CPU quota requires the existing systemd scope mode; native jobs retain process-group resource monitoring.

Every ten seconds, and after reconnect/restart, the authority reconciles up to 16 managed leases. It binds and verifies job ID, PID, process start ticks, boot ID, heartbeat, other project jobs and deployment records. A fresh PID/process-group disappearance proof plus SQLite compare-and-swap releases the lease. Concurrent native admission is fenced at the shared jobs.Manager entry point; MCP fs_patch also rejects a held managed writer. Resource disappearance does not change an UNKNOWN job into success and does not authorize replaying an interrupted task.

Explicit reconciliation through cached schemas:

```json
{"command":"mcpshka","args":["lease-reconcile","/path/to/project"],"workdir":"/path/to/project","idempotency_key":"unique-reconcile-key"}
```

Read the returned bounded receipt via `job_output` using `limit`, then inspect `project_lease_status`. Replay keys describe the original operation; fresh status is authoritative.

HOLD is intentional when owner binding is missing/mismatched, another job/deploy remains pending, scope ownership needs independent reconciliation, authority cannot be read, or a start was interrupted before durable process identity existed. Manual/legacy leases require the holder's token-bound release or an explicitly authorized administrative reconciliation. Do not delete SQL records, infer death from age, kill foreign processes or retry UNKNOWN work with a fresh key.

Both acquisition caches validate the current holder token, including the outer mutation-contract receipt. A released or replaced lease cannot be resurrected by replaying an old acquisition key.

## P6 recovery fairness (0.1.65)

### SQLite capacity and unresolved retry receipts

`state_status` reports the effective SQLite page limit, occupied percentage, reusable freelist pages, and remaining page budget. `SQLITE_CAPACITY_HIGH` means occupied pages exceed 80% of the effective limit. Freelist pages are counted as usable capacity. Check the capacity and unresolved receipts before scheduling more workload when this warning appears.

The 72-hour retention window applies only to finished receipts for allowlisted operations. Age alone never resolves a pending effect. Use the original tool and retry identity with `mutation_probe` or the tool's named reconciler. Confirm the effect from durable job results and authority records. Receipts proven to precede any effect can be discarded by the normal handler. A verified completion can become eligible for its ordinary retention policy. Missing proof, stale ownership, and protected operations retain the original receipt and fence. Avoid starting replacement attempts while any such evidence is unresolved.

If the before-audit write fails on a fresh request, the handler never ran. The tool may release that unstarted receipt so the same request can be retried after audit is healthy. An older pending receipt remains held.

Bounded background UNKNOWN reconciliation now rotates deterministically through pending records. A persistent HOLD (missing process identity, active owner, or scoped ownership) no longer repeatedly occupies the same bounded inspection page while later records wait. Each attempt still requires independent process-disappearance evidence; reconciliation never fabricates an exit result, releases an unverified owner, or retries its mutation. This is one P6 recovery improvement, not the P6 chaos acceptance gate.

## P6 isolated fault-injection acceptance (0.1.66)

Tests `TestP6ChaosSupervisorCrashUnknownNoDuplicate` and `TestP6TunnelAndSessionLossKeepsDurableJob` run 3 isolated trials each. The first kills a real supervisor subprocess mid-job, keeps the bounded worker alive, verifies PID/boot/start-time identity, refuses replay while the owner is live, reaps it through a Linux subreaper, persists UNKNOWN with verified resource release, and confirms the external side effect happened exactly once after a second manager restart. The second disconnects an independent in-memory transport and AI session while a real bounded job completes, then verifies the committed result persists after reopening the state store. Tests never kill the production MCP server or tunnel.

These six trials prove local P6 recovery behavior; they do **not** prove redundant off-box control paths or production tunnel failover. Those require the separately deferred P0/P4 external OOB canary and cannot safely be simulated by severing the only verified MCP management path.

## P7 managed workflow handoff and roadmap evidence (0.1.67)

Read-only `mcpshka roadmap-scan <allowed-root/ROADMAP.md> [sha256-pin]` reuses the bounded scanner and returns a compact untrusted candidate; a ROADMAP checkbox is not authority to mark any phase DONE or execute a mutation. A wrong SHA pin, oversized file, symlink or ambiguous markdown fails closed.

`TestManagedWorkflowHandoffAcrossMCPSessions` uses real MCP in-memory client/server sessions backed by a dedicated SQLite authority. After AI client disconnect, a second client retries the *same* managed key and receives the original job ID. A competing writer gets HOLD; that failed idempotency key remains failed even after the original job exits, preventing surprise execution. After verified lease recovery, a **new** key is accepted with a different job ID; both jobs reach DONE. This is one canary-level P7 vertical slice, not acceptance of the whole multistep Workflow Engine.

## P7 immutable bounded handoff artifacts (0.1.78)

The projectprofile core now implements bounded (8 MiB) content-addressed handoff artifacts: exclusive publish into a persistent per-workflow directory, SHA-256 receipts, verification of type/size/hash, and fail-closed rejection of tampering, overwrite, oversize and path traversal. Caller must authorize the persistent root using the existing policy; this core is not yet exposed as a standalone MCP tool. Cross-session operational canary and automatic workflow executor remain outstanding.

## Cross-session artifact and fs_read interruption acceptance

Codex-local `/tmp` is not a shared filesystem contract. AI handoff must use a policy-allowed persistent project artifact location, scoped per workflow and job, with atomic publish (temporary file followed by rename), restricted permissions, SHA-256, size cap and durable metadata. Consumers must verify the hash and pinned workflow identity; missing files are HOLD, not empty results. A failed read-only `fs_read` may be retried after reconnect with bounded output and content hash verification; a failed mutation requires reconciliation before retry. These are **outstanding operational acceptance tests**, not DONE.

## P7 pinned job receipt identity (0.1.77)

`ReconcilePlannedJob` validates the recorded bounded job against the pinned plan fingerprint, command, argument vector and canonical worktree before accepting a terminal receipt. Integration tests reject unrelated commands, worktrees and plan drift. This is a safety guard, not a complete autonomous workflow executor; P7 acceptance remains open.

## P7 failed-job HOLD reconciliation (0.1.76)

A terminal failed, cancelled or timed-out bounded job receipt now durably fences its running checkpoint in HOLD; UNKNOWN checkpoints are reconciled to HOLD with a bounded evidence receipt. Running/UNKNOWN job outcomes still block any transition. An integration test runs a real failing bounded job and verifies durable HOLD instead of retry or false DONE. Automated multi-stage execution and compensation remain outstanding.

## P7 real bounded-job receipt verification (0.1.75)

`ReconcileCheckpointJob` reads the actual MCPshka jobs manager receipt for the job ID durably bound to a running or UNKNOWN workflow step. Only a terminal DONE with exit code 0 may advance the stage; missing, running, UNKNOWN, failed, cancelled or timed-out jobs are HOLD. An integration test runs a real bounded `true` job and verifies the recorded receipt and rejection of replay. This does not yet implement autonomous multi-stage orchestration or compensation.

## P7 durable job identity association (0.1.74)

`BindCheckpointJob` atomically binds a real bounded job identity to a running workflow step, rejecting stale writers and duplicate bindings. Advancing to a new stage clears the previous job identity. The CAS test checks that a second AI cannot replace the first job. The managed executor is not yet wired to this journal, and automated compensation is not yet provided; this release does not claim P7 DONE.

## P7 fail-closed resume and completion guard (0.1.73)

`ResumeCheckpoint` atomically moves an interrupted RUNNING stage to UNKNOWN and blocks replay until external job/deploy evidence is reconciled. `FinishCheckpoint` permits terminal success only after the pinned CLEANUP stage is DONE. A SQLite canary covers every ordered stage and simulated interruption, stale-writer rejection, plan drift and evidence-backed reconciliation. This tests the state machine, not autonomous command execution, compensation or live deploy interruption.

## P7 pinned workflow start through MCP (0.1.72)

The `workflow_checkpoint_start` MCP mutation resolves a named project workflow through the trusted profile resolver, pins the resulting plan fingerprint to a durable SQLite workflow identity, and rejects conflicts with a different plan. The mutation contract uses a read-only state probe before creation and a workflow checkpoint probe afterward; idempotency prevents duplicate starts. It does not execute the plan. The MCP integration test checks repeated starts and the pinned identity. End-to-end workflow execution, recovery, compensation and canary remain outstanding.

## P7 evidence-backed reconciliation primitive (0.1.71)

The internal `ReconcileCheckpoint` operation accepts only UNKNOWN/HOLD/FAILED checkpoints, requires bounded nonempty reconciliation evidence, and atomically records either DONE or HOLD without rerunning a job or advancing a stage. Stale writers are rejected. This is a foundation for a future guarded MCP reconciliation tool; the evidence is not yet independently verified by a trusted provider and therefore the operation is deliberately not exposed as a public mutation endpoint. Targeted and full Go tests cover this state transition.

## P7 checkpoint visibility through MCP (0.1.70)

The read-only `workflow_checkpoint_status` MCP tool loads one durable workflow checkpoint from the existing SQLite authority without claiming, executing or replaying the step. The in-memory MCP transport test verifies two successive AI sessions see the same pinned workflow identity and revision. Same-step transitions now enforce pending→running and running→done/failed/unknown/hold, rejecting pending→done and stale writes. No automatic execution or rollback is claimed; a separate guarded workflow executor remains necessary.

## P7 durable step checkpoint foundation (0.1.69)

The projectprofile checkpoint API now stores a workflow identity, pinned execution-plan fingerprint, step, status and monotonically increasing revision in the existing SQLite WAL authority. Create is idempotent only for an identical pinned plan. A step transition uses a compare-and-swap record update: stale writers and conflicting plans fail closed. The fixed stage order is inspect → modify → test → review → commit → build → deploy → reconnect → verify → cleanup. Steps cannot be skipped, and UNKNOWN/HOLD/FAILED block forward progress pending an explicit reconciler. A test reopens the SQLite database to verify AI handoff durability, stale-writer rejection, and UNKNOWN hold. This foundation is not yet wired into the MCP execution entrypoint and does not provide automatic compensation or end-to-end P7 acceptance.

## P7 pinned execution plan fingerprint (0.1.68)

Resolved project workflow plans include a deterministic SHA-256 fingerprint of the canonical JSON execution contract (project root, profile source/name, workflow, command, arguments and resource limits). A fresh AI session can compare this fingerprint against its earlier plan before starting work, detecting profile or scope drift instead of silently trusting a stale description. The hash is evidence, not an authorization token; the runtime must still enforce lease, policy, idempotency and deploy checks. This is an incremental P7 slice, not a completed multistep workflow/recovery engine.
