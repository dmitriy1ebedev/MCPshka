# Workflow and Health acceptance

Source baseline: b26289f6d1cb801b25f73108d6f905284b8d3ba5.
This document describes source fixtures, not a production release receipt.
The running control plane reported 0.1.196 at discovery; these changes do not
change that installation or the source version.

## Recovery matrix

Run each Go package with project_test, packages set to the package below and
run set to the named test. The final unfiltered project_test includes them all.
Every fixture uses temporary data; none cuts the production tunnel.

| Fault | Source fixture | Durable identity | Observed assertion and required recovery |
| --- | --- | --- | --- |
| Disconnect / cold session | internal/workflow: TestPipelineSurvivesColdSessionAndNeverDuplicatesStages | canary-run; workflow-<sha256(run ID + colon + stage index)[:32]> receipts | Reopened manager completes the original ten-stage run; event log contains each stage once. Another writer is refused. |
| MCPshka restart / lost exit | internal/workflow: TestLostExitAfterRestartCompensatesWithoutInventingSuccess | original Run.ID / JobID / RecoveryCause | Unknown exit is never invented as success; pinned compensation and finalizer run, original mutation is not replayed. |
| Timeout / lost launch reply | internal/workflow: TestLostLaunchReceiptNeverReplaysOrUnlocks; TestGatedMissingReceiptAutomaticallyCompensatesWithoutReplay | original run, launch supervisor, reserved JobID | Missing receipt retains HOLD or verified gated compensation; age alone never unlocks a writer. |
| Repeated command | internal/releaseprep: TestNativeReliabilityMatrix; test_core129_upgrade.py:test_repeat_command_and_lost_launch_reply_do_not_dispatch_twice | sealed plan operation ID and immutable root intent | Exactly one dispatch, same receipt after retry; unknown effect is inspected. |
| Incomplete installation | same wrapper; test_core129_upgrade.py:test_incomplete_installation_or_changed_candidate_keeps_hold | original operation ID and component digests | HOLD before replacement; changed or missing installed components are not guessed. |
| Controller + broker rollback | same wrapper; test_coordinated_upgrade.py:test_failure_rolls_back_both | coordinated-release plan and audit events | Both original byte images restored after candidate health failure; backup corruption / foreign content is not rollback authority. |
| Stale accepted / deployment_pending | internal/deploy: TestReconcileSurvivesRestartWithoutReplayingOrClearingStaleAccepted | persisted DeploymentID, correlation ID, deploys/<ID>.json | Repeated cold read preserves the exact accepted receipt and pending blocker until an independent terminal helper receipt exists. |
| Lease conflict / lost token | internal/mcpserver: TestColdLeaseRecoveryUsesOriginalAcquireReceiptThenHeartbeat; internal/projectlease: TestIndependentSiblingWorktreeLeases | original-acquire, LeaseID, ExecutionID, Generation | Cold same-owner/input/key recovers the original token, then heartbeat; new acquire and foreign-owner replay fail. A sibling worktree has an independent lease. |
| Codex ID / metadata / reply loss | internal/rootbroker: TestCodexCreatePersistsIdentityBeforeProbeAndNeverRecreates; TestCodexUnknownReplyAndProbeFailureNeverReplayCreation; native wrapper test_codex_session_client.py | creation_key, owner-bound intent ID, returned thread ID | IDENTIFIED is durable before policy probe. Known identity + incomplete proof is HOLD; missing returned identity is UNKNOWN. No second create RPC. |

The native Python helper suites are reached by
internal/releaseprep/reliability_test.go, so they run inside the same bounded
project_test queue. Their source files are read to invalidate the Go test cache
when the helper or its fixture changes.

## Mission continuity

mission_start can bind an already approved workflow_id to exactly one scope.
The executor keeps the original twelve pinned plans (ten stages, compensation
and finalizer), claim, reserved job IDs and resource limits. Binding does not
launch an agent, acquire another writer or create a replacement workflow.

A terminal event is durably queued before releasing the workflow claim.
The callback rechecks current HEAD/tree, capability contracts and acceptance
before checkpointing the bound mission. A completed workflow without acceptance
does not complete the mission. The completion outbox survives a daemon restart;
a lost callback result is delivered again with the original workflow ID. Pending acceptance retains the outbox event for the existing maintenance loop; it never launches another workflow.

Agent dispatch proposals separately retain their original dispatch_key,
step_fingerprint, approved path, inputs and contract revision through a changed
HEAD and cold resume. INTENT means proposal only; it is never an execution ACK.
All already proven checkpoints advance in one resume without another AI turn.

## Health contract and measurement

The collector is on demand: no scheduler, background network probing or quick
retry loop. One collection runs at a time; callers share a 15 second cache.
The collection deadline is 16 seconds and total output is at most 16 KiB.
Probe deadlines require context-aware adapters. Missing, malformed and old
evidence is never healthy.

Conditional detection budget: **N = 32 seconds**, provided an external consumer
requests a snapshot at least once per second. This is 15 seconds cache +
16 seconds collection + 1 second consumer interval. Without polling, an on-demand
snapshot has no autonomous detection-time guarantee.

internal/healthlayer:TestControlPathDetectionBudget measures the real cache
interval against an isolated in-memory path-loss fixture. The existing
TestControlPathFaultDetectedAfterCacheExpires also uses a local HTTP server.
Neither test changes any production route or tunnel.

TestHealthLoadBudget measures 1024 cached requests from 32 concurrent callers:
process CPU, allocated bytes, process maximum RSS, output size and fixture disk
files. Assertions: one probe call, at most 2 CPU seconds, at most 32 MiB
allocations and at most 16 KiB output. TestSingleFlightCacheAndImmutableEvidence
and TestCancelledWaiterDoesNotLaunchAnotherProbe cover shared collection and
cancellation. These are fixture budgets; VPS/container/systemd collection under
external host load still needs a measured acceptance run.

Storage health reads a bounded local journal, not remote quota or auth APIs.
A downloaded/hash/AEAD/manifest-verified backup supplies freshness and replica
evidence; credential presence alone never proves provider authentication.
Quota, provider availability and restore-drill data without an authoritative
source remain explicit UNKNOWN/null. Backup freshness is stale after 24 hours.

Capability exposure/schema/index health is separate from control-path health.
A refreshed tool list with unchanged names is insufficient if schemas changed.
Legacy name-only catalogs retain unknown schema evidence. An index must match
the Knowledge Core revision; unchanged canonical data does not make an old,
still matching index stale merely because its build date is old.

## Item evidence

DONE below means implemented and covered by source fixtures in this branch.
The P7/P8 phase and production release are not declared accepted by this table.
The unfiltered project_test receipt for the containing clean commit belongs in
the handoff, so source and execution evidence can be compared exactly.

| ROADMAP line | Status | Source commit and test |
| --- | --- | --- |
| 688 | PARTIAL | 4fed54e, TestNativeReliabilityMatrix + recovery matrix above; live VPS matrix remains. |
| 689 | PARTIAL | 4fed54e, TestCodexCreatePersistsIdentityBeforeProbeAndNeverRecreates + native metadata fixtures; real interrupted Codex cycle remains. |
| 690 | PARTIAL | 51705e5, TestColdLeaseRecoveryUsesOriginalAcquireReceiptThenHeartbeat; real root/Face ID recovery remains. |
| 694 | DONE | 1655bb6, internal/mission/bootstrap_test.go:TestColdMissionDiscoversBeforePolicyAndAcceptance. |
| 695 | DONE | 68f1c9c + a027292, internal/storagefabric/workflow_test.go:TestBackupWorkflowCheckpointAndRestoreRemainStaged. |
| 696 | DONE | d496681 + b589209, TestClientCatalogDetectsSameNameSchemaDrift, TestBoundWorkflowFingerprintCannotChangeAfterHandoff, TestBackupWorkerRejectsFutureContractBeforeWriting. |
| 700 | DONE | b589209 + e07660a, internal/workflow/mission_acceptance_test.go:TestBoundMissionWaitsForAcceptanceAndResumesFromColdOutbox. |
| 701 | DONE | 51705e5, TestColdLeaseRecoveryUsesOriginalAcquireReceiptThenHeartbeat; existing workflow/projectlease tests prove CAS, sibling scopes, process fencing and no UNKNOWN replay. |
| 702 | DONE | e3f05d3 + e07660a, TestMissionRetainsOriginalDispatchKeyAfterCommitAndColdResume, TestCompletionOutboxSurvivesColdRestartWithoutReplay. |
| 703 | PARTIAL | Existing paired native gates + 4fed54e helper fixtures; combined pinned review/CI/release and installed identity/smoke remain with the release owner. |
| 710 | DONE | 2008dcc, health_tools.go fixed collector + TestHealthNativeReadOnlyAndCachedCompatibility. |
| 711 | DONE | Existing laneprovider/health.go read-only authority; 2a5f5a5 TestCriticalContainerAllowlist; no investigation/controller added. |
| 720 | DONE | 2008dcc + 2a5f5a5, TestCombinedOutputAndInvalidConfiguration, TestSingleFlightCacheAndImmutableEvidence. |
| 721 | DONE | Existing Knowledge Core/revision checks + 2008dcc TestCapabilityHealthCannotAcceptFutureIndexTimestamp. |
| 722 | PARTIAL | 2a5f5a5 TestControlPathDetectionBudget (N=32s, conditional on polling); real VPS measurement remains. |
| 723 | PARTIAL | 2a5f5a5 TestHealthLoadBudget and cancellation/single-flight fixtures; all real adapters under external load remain to be measured. |
| 724 | PARTIAL | This source evidence and local verification; independent combined release review and production smoke remain. |
| 729 | DONE | 68f1c9c TestStorageHealthUnknownAndVerifiedEvidence; 2008dcc TestBackupHealthDoesNotTurnMissingAuthorityGreen. |
| 730 | DONE | d496681 + 2008dcc, client schema tests + TestCatalogHealthHasBoundedClientSummary, separate capability_catalog source. |
| 735 | DONE | Existing mission/status.go:Evaluate + TestStalledHeartbeatNeverAuthorizesWriter, TestMissingEvidenceAndIndependentWork; native bound workflow progress keeps the original ID/fingerprint. |

## Current authority observation

On 2026-10-26 the installed state_status returned mode sqlite-v1.
Read-only project_lease_admin_review for this worktree reported the original
LeaseID/ExecutionID/generation with owner_bound=false, fencing_state=not_verified,
state=UNKNOWN and requires_administrator=true. This is a fail-closed takeover
review, not a failure of the token-bound interactive holder or a recovery grant.
No administrative transfer, fence repair or router session was attempted.

## Remaining live acceptance

- Run a real new Codex intent through client interruption and daemon restart on
  the disposable VPS, recording original creation key, intent and returned thread
  ID. Do not create a replacement for UNKNOWN.
- Run the existing disposable-root native administrative lease recovery suite
  under real systemd/Face ID authority. Its explicit E2E guard must remain set;
  ordinary unit PASS does not mean that test ran.
- Measure the 32-second conditional control-path budget and CPU/RAM/disk/output
  with all real adapters on the VPS. Keep the production tunnel connected.
- The release owner must perform isolated review, combined integration, pinned
  build/CI and protected-core/rollback gates, then installed version/hash,
  production smoke and clean/held-state evidence for 0.2.1.
