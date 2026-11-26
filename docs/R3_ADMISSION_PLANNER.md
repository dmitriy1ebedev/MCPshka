# R3 offline admission planner (non-executable)

Scope: `internal/codexcoord/admission.go`. The planner makes **advisory decisions only** from pinned task identity, model/reasoning catalog and externally supplied observations. It never starts Codex, mutates Git, obtains a lease, or asserts that the input observations are trustworthy.

## Decision semantics

- `HOLD` for an unsupported model/reasoning pair, invalid bounded resource budget, changed session/worktree/ROADMAP identity, unknown owner, busy/unknown session, uncertain writer lease, missing healthy management path, or unknown/exhausted provider quota.
- `READY_FOR_APPROVAL` only after every supplied preflight signal is affirmative. This is **not execution permission**: `dispatch_enabled=false` unconditionally, and separate trusted gates, actual lease ownership, verification of evidence provenance and worker receipts are still mandatory.
- Stable task ordering by ID; duplicate identities and simultaneous use of a session or worktree are held; one blocked task does not block unrelated scopes. Admission is bounded to 32 tasks and a declared maximum concurrent count.
- The intent fingerprint binds the task, version, model/effort, scope, message and CPU/memory/time/output budgets. The fingerprint does not certify observations, a real Codex model, or a completed task.

## Offline acceptance

`go test -race -p 1 ./internal/codexcoord` verifies three independent tasks, deterministic re-evaluation, blocked-session non-starvation, duplicate/conflict rejection, unknown quota, unsupported reasoning, bounds and absence of implied dispatch. `go vet -p 1 ./internal/codexcoord` must also pass. No live Codex or production operations are part of this acceptance.

## Integration boundary

Wire this planner only through MCPshka's bounded, read-only interface after the current `codex_intent_*` implementation is reconciled. Do not accept planner-selected tasks as actionable until evidence origin/owner verification, quota discovery, idempotent external RPC receipt, guarded recovery and the protected release path exist. Tailscale/Face ID are excluded.

## Durable checkpoint — 2026-10-13 (HOLD, not DONE)

- Clean reviewed admission-planner commit: `b91ab317f6041609e30f7a4dee8ef250343ec9fb` on `r3-safe-queue-views-20261013`.
- Additional uncommitted work in this isolated worktree: `internal/mcpserver/codex_admission.go`, `internal/mcpserver/codex_admission_test.go`, registration change in `internal/mcpserver/server.go`. This is a **read-only simulation**, not production or Codex dispatch.
- Offline MCP API targeted job `mv6q7xbg-4j` timed out at 240 s during Go package build (no stdout, exit=-1). Job is stopped; package tests are **NOT accepted**. The prior `codexcoord` targeted/race/vet were successful.
- New job attempt was rejected before execution: `state_status` reports `state listing budget exceeded`. Do **not** bypass this security gate with another transport, hand-run shell or blind retry.
- Canonical `main` HEAD observed as `66a03be57434fabcec52d9c19e0b51f6bf7096dc` with uncommitted parallel `codex_intent_*` work. Do not merge or overwrite before ownership reconciliation. Production confirmed `0.1.103`, rollback `0.1.102`; candidate `0.1.104` exists but is not deployed.
- Next safe action: reconcile the protected recovery route, test the scalar-count fix on an isolated state store, then perform an authorized guarded release of the fix; verify `state_status`, state integrity, service health, tunnel, rollback and Git provenance. After that, resume bounded R3 tests/review/commit and integrate only once the foreign writer is reconciled. Keep Codex disabled and Face ID/Tailscale unchanged.

## Additional checkpoint — state observer saturation (2026-10-13)

**Proven root cause from source:** `internal/mcpserver/state.go` used `db.List("")` merely to count records. `internal/statestore/store.go` aborts `List` after 10,000 keys. This fails closed as intended for enumerations, but accidentally makes `state_status` fail. Mutation contracts use `state_status` as a before-probe, so protected `exec_start` is held with `state listing budget exceeded` even when jobs are stopped.

**Isolated uncommitted fix:** `statestore.DB.RecordCountContext(ctx)` performs read-only `SELECT count(*)` with the existing 5-second SQLite context and bounded output. `state_status` uses it instead of enumerating keys, and a regression fixture constructs 10,001 records while confirming that `List("")` remains denied. No global cap is raised; no state records are deleted or repaired. These changes exist only in `r3-safe-queue-views-20261013`; production still runs `0.1.103`.

**Additional integration hardening:** `codexcoord.Assess` now validates persisted queued tasks separately from new task enrollment because the parallel `ValidateShape` change rejects nonzero revisions. A regression fixture uses a real SQLite-queued receipt. This separation prevents every healthy queued task from appearing `HOLD` after the future merge.

**Verification and release gates:** The scalar count, additional admission regression and read-only MCP preview remain UNTESTED following the root preflight block. No new job/commit/build/deploy is permitted through a failed state observer. Independent `codexcoord` race/vet tests from before these edits are valid only for the previous commit `b91ab31`, not for the uncommitted patch. Authorized recovery must restore the trusted observation path and repeat checks before commit/deploy. The production `0.1.103` plus rollback `0.1.102` was verified earlier; current source is dirty in both worktrees.

## Recovery-route verification — 2026-10-13, 13:57 UTC+3

- `state_status` still fails `state listing budget exceeded`; root cause is the exact `db.List("")` + 10,000-key limit, not a stalled test process.
- Contract analysis confirms that `exec_start` and `project_build` have no initial job ID, so their `job_status` before-probe falls back to `state_status`. Launch cannot be retried using an alternate key or command to evade the state observer.
- `mutation_preflight(kind=self_deploy)` returned `GUARDED` with preserved tunnel, detached-helper rollback, and production verification required, **but this is not approval to deploy unbuilt or untested source**. The only available prebuilt `0.1.104` candidate predates the scalar-count patch and cannot fix this failure.
- `rescue_status` is unavailable because `/usr/local/libexec/mcpshka-rescue.py` is absent; no recovery action was attempted. `project_lease_admin_review` reports RELEASED / no takeover required.
- `project_verify` confirmed service, tunnel, `healthz`, tunnel readiness, DNS, HTTPS, and rollback: 8/9; the sole failure is dirty canonical Git. Installed `0.1.103`, rollback `0.1.102`. No production mutation occurred.
- The recovery and test blockers are external to this isolated patch. Next authorized step is to restore a **trusted** control-plane execution/recovery channel or repair its state observer using an approved operator recovery procedure, *not* to bypass the failed preflight. Then run isolated statestore and Codex admission tests, review, commit, build the next available version, guarded deploy, reconnect and verify.

## Rescue packaging regression — 2026-10-13

- Verified the `packaging/native/rescue.py` helper is present in source and is the exact executable expected by `internal/rootbroker/rootbroker.go` at `/usr/local/libexec/mcpshka-rescue.py`.
- Verified that the original `packaging/native/install-broker.sh` validated and installed `state-store.py`, `state-backup.py` and Codex bridge, but **omitted rescue.py**. This explains why the production `rescue_status` tool rejects its missing helper. The inspection does not establish when or by whom the helper went missing.
- Added `RESCUE_SOURCE`/`RESCUE_TARGET`, included the script in root-owned release-input validation, and added an install step. No rescue timer is enabled or existing services restarted by this source change.
- Added `test_rescue_helper_is_preflighted_and_installed_without_enabling_timer` and expanded the installer-input manifest test.
- Status of this new rescue patch: **UNTESTED, UNCOMMITTED, UNDEPLOYED** because the installed `state_status` error makes `exec_start`, `project_test` and `project_build` fail their before-probe. The existing `0.1.104` candidate does not include these fixes. Root broker reinstall is privileged and additionally requires its own reviewed release/approval, not a workaround for failing observer preflight.
- No Codex turn/RPC, broker install, service restart, deploy or SQLite deletion was performed. Face ID/Tailscale remains outside scope.
- Once the trusted execution gate is restored, independently run Go statestore tests (including >10k), Codex admission targeted/race, MCP preview in-memory, `python3 packaging/native/test_install_broker.py`, `bash -n packaging/native/install-broker.sh`, `go vet`, full suite and review. Reconcile the canonical dirty `main` and original worker ownership before integration, then commit/build/guarded-deploy with production+rollback receipts.

## R3 offline validation receipts (2026-10-13)

The versioned validation scripts and durable stdout/status receipts are on the host under
`/root/repos/mcpshka/.agents/tmp/r3-validation-v{2,3,4}.{sh,log,status}`.
Do not inline executable script bodies into this roadmap document.

- v2: FAIL before Go tests because the systemd job had no HOME/GOCACHE environment.
- v3: targeted SQLite (>10,000 state keys), read-only Codex admission and MCP preview, rescue installer, shell syntax, go vet passed; full Go test suite failed in root-only deploy, executor and verify fixtures.
- v4: Git whitespace, formatting, targeted SQLite/admission/MCP, rescue installer, vet all passed. Isolated non-root deploy/executor/verify tests all passed, as did every full-suite package other than internal/mcpserver.
- Exact v4 blocking failure: `TestManagedWorkflowHandoffAcrossMCPSessions` expects a competing writer to be rejected after an asynchronous MCP reconnect; its fixture runs `sleep 1`, and the original writer can finish and have its lease verified released before the competing request. The test needs deterministic active-writer synchronization without weakening production fencing.
- v4 final status `FAIL:1`; full-suite failure prevented subsequent race tests and binary build. No commit or release was made. Root-owned production MCPshka `0.1.103`, retained rollback `0.1.102`; tunnel is healthy.
- The only existing candidate `0.1.104` predates the SQLite count fix and is not suitable for deployment. The canonical main worktree has other actors' uncommitted code; do not overwrite.
- Next acceptance: authorized focused test fixture repair, targeted/full/race, reviewed commit, trusted build, guarded deployment, production and rollback verification, cleanup. The protected execution preflight still fails until production `state_status` uses bounded SQLite scalar count; do not circumvent the gate.

## Offline candidate validation v5 — prepared, pending operator launch

- Source-only change: internal/mcpserver/project_profile_test.go now gives the handoff fixture an 8-second worker, a 9-second bounded per-job deadline and 12-second completion waits. This affects tests only; writer fencing and production policy are untouched.
- Parent test file SHA-256: c78a26d725cde2e6a025a45f4e6679621dcee44da9dd123d3c47272f669ad553.
- The previously used v4 runner is intact (SHA-256 4d8f17f33e8145689f84df11e00c7d6b8ab114f13114d6cffb0369a4aa863484). A new v5 can be created beside it with literal v4-to-v5 substitutions, leaving v4 receipts unchanged.
- MCP artifact ingress rejected source delivery because its preflight still encounters state listing budget exceeded. Protected execution and deployment must remain unavailable until the production scalar-count observer is repaired through an authorized recovery process.
- Acceptance still required: fresh isolated nonroot full Go suite, focused regression repeat, race tests, source review, commit, a trusted version-pinned binary build, permitted guarded deploy, production/rollback verification. Do not claim 0.1.104 shipped based on offline checks alone.
