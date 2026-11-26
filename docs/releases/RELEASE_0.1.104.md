# MCPshka 0.1.104 — R3 Codex task coordination foundation

Release source: R3 commit `30c152d7aa63929c1744142deaab0aacada83a93` plus the 0.1.104 version increment.
Status: CANDIDATE ONLY; full R3 and production enablement are NOT DONE.

## Scope and guarantees
- Durable SQLite task queue and CAS revisions with per-task model/reasoning intent, pinned worktree, ROADMAP digest and session identity.
- A task reservation is persisted before any possible dispatch. Independent claims protect the session and hashed worktree; repeated identities reconcile without a second dispatch.
- A lost response or partial two-claim record remains HOLD. No timeout-based takeover, false DONE, branch deletion, or unverified lease release.
- Queued model and reasoning can be reconfigured by CAS. A reserved or live turn cannot be silently reconfigured.
- Read-only Codex bridge retains existing queue and idle-turn behavior. Explicit model overrides are fail-closed pending verified live protocol/ownership acceptance.
- No Codex worker was started and no Codex runtime/model used for acceptance in this release.
- Face ID/passkey/Tailscale, broker installation, VPN, DNS, LANE, HA and other protected services remain outside scope.

## Acceptance and deferred gates
- Three independent model-configured tasks and restart/replay behavior in isolated SQLite fixtures; conflicts and reserved HOLD tested, no false DONE.
- Targeted Go/race, complete Go suite and go vet; offline Python bridge and Git diff review.
- Missing before production R3 DONE: trusted live model catalog, real per-task dispatch and worker receipt, owner-bound turn handoff and live switch verification, 3 real scoped tasks, failure recovery against real Codex, tests after integration, trusted broker/helper activation gate.
- Candidate build does not authorize self-deploy. Protected root broker changes require separate human approval.
- Pending Face ID iPhone/WebAuthn acceptance remains unrelated and must not be modified.

## Next safe action
Inspect live Git/jobs/leases and candidate/prod version, verify source provenance and build checksum, then finish isolated task dispatcher and authorized model/turn protocol. Keep production 0.1.103 and rollback 0.1.102 until all release gates permit a guarded deploy.

## Historical control-plane blockage checkpoint (superseded by 0.1.105)
- Production verified: 0.1.103; retained rollback: 0.1.102; local MCP health/tunnel ready, DNS and HTTPS checks passed. No deployment was scheduled.
- Root cause of the newly observed job-admission failure: `state_status` materializes every SQLite key with `db.List("")`, but that API deliberately rejects more than 10,000 records. The resulting `state listing budget exceeded` causes the mutation before-probe to HOLD even bounded Go test/build jobs. Never widen the protective List budget or retry UNKNOWN mutations.
- Local source change, **not built, tested, committed or deployed**: `internal/statestore/store.go` adds read-only SQL `Count(prefix)`; `internal/mcpserver/state.go` uses it for `state_status`; `internal/statestore/store_test.go` includes a >10,000-record regression that keeps `List` bounded.
- Local R3 read-only visibility change, also untested and undeployed: `internal/mcpserver/server.go` registers `registerCodexQueueTools`, with registration in `codex_intents_test.go`. Existing untracked R3 files and changes were preserved; do not clean/reset the dirty worktree.
- The native MCPshka test workflow still returns `before probe failed; request held: state listing budget exceeded`. Remote Desktop Commander reports its monthly call quota exhausted, so it cannot be used for independently authorized emergency maintenance in this session.
- Next action after restoring an authorized bounded execution path: reconcile dirty Git ownership; inspect/review all R3 diffs; format and run targeted/race/full Go tests and vet; finish authenticated live model catalog, scoped dispatch and worker receipts, recovery and three authorized canaries; obtain any protected-core activation approval; commit only reviewed changes; build a new candidate from its committed revision; perform guarded deploy, reconnect, production verify and rollback checks. Until then this remains BLOCKED, not DONE.
- Emergency maintenance bootstrap prepared 2026-10-14 in `packaging/native/test_state_store_namespaces.py` under explicit `--repair-control-105` entrypoint (the normal unittest entrypoint remains unchanged). The embedded compressed script is checked against SHA-256 `71f55caf83917322702b01aab889262f949f55df11679fac0d4237c4e39cd965`; original carrier SHA-256 `8df3af04acd813021c7f32ff08262f24640d11aabb27368cf7eacd09dbfac7ed`; **revised Go environment carrier** SHA-256 `e2bc1d52b6483d4edba90b6ae7cf15219db7109be4559e38fb284b1165a1eebc`. The original was executed once by an independently authenticated root operator and stopped *before candidate build/deploy* at isolated Go unit startup because GOPATH/GOMODCACHE was not forwarded into `systemd-run`. The carrier was revised to pass GOPATH, GOMODCACHE and GOCACHE from read-only `go env` into transient units, retaining GOPROXY=off. The Go-environment carrier was executed by the root operator; targeted `internal/mcpserver` tests returned HOLD because Go `t.TempDir()` used `/tmp`, whose small filesystem failed the artifact inbox's unchanged 3GiB/10% safety reserve. No candidate was built or deployed. The next carrier SHA-256 is `d0d7fbacf80f3a32985359809909fa6f2c22d7518c71680f26710ebde7473670`: it uses a private test scratch under the existing `/var/tmp` isolated checkout and checks `disk_usage` for the full safety reserve plus 256MiB before every Go step. The embedded post-patch Python source SHA-256 is `ff8e6ea8c9e93e8427e65018fc34149320ed9285ee7fa4eeefd08022b9fcd2c2`. This next carrier has **not yet been executed**. Independent root execution (only with a pinned trusted file copy) performs isolated test/vet/build of a minimal `0.1.105` recovery from committed `0.1.103`, applies an exact root-owned SQLite helper count correction, then invokes the installed guarded self-deployer with rollback. Its conditions fail closed if pinned revision/production state differs; it does not deploy unfinished R3. After successful repair, remove the emergency entrypoint and complete R3 as a later release.

## Reconciled state

0.1.104 was never accepted as a production release. Recovery 0.1.105 was committed
as `6b3f3b3` and deployed with receipt `93a096cf-67f9-4f4a-8202-33ff253fb172`;
production retains rollback 0.1.103. See RELEASE_0.1.105.md for the recovery scope.
The historical bootstrap above is preserved as provenance, not an executable
recovery instruction. Its source carrier was removed from the normal test file.

R3 admission/model previews, durable planning intents, queue inspection and
regression fuzz seeds are consolidated into main. Validation is shared, revision
exhaustion fails closed, and intent capacity admission is atomic across SQLite
clients. The rescue helper is included in installer preflight and installation.
These source changes do not prove real R3 dispatch or production activation.
Trusted model/session/lease admission, execution/handoff acceptance, full release
gates and a coordinated broker/helper/binary rollout remain required.
