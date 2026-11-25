# MCPshka invariants (MCP-058)

Each invariant of a durable state machine names the regression tests that
prove it. `internal/invariants` fails CI when a named test no longer exists,
so an invariant cannot silently lose its evidence. Format of a line:
`- I-<area>-<n>: <statement> — `Test...`, `Test...``.

## Project lease (single writer)

States: FREE → HELD(generation, token) → RELEASED | STALE(reconciliation) → FREE.

- I-LEASE-1: at most one writer holds a repository lease, across processes and service instances — `TestLeaseIsAtomicAcrossServiceInstances`, `TestConcurrentResumeCannotCreateTwoWriters`
- I-LEASE-2: a lease survives a restart; heartbeat continues it without reclaim — `TestTrackedLeaseHeartbeatSurvivesRestart`, `TestSQLiteLeaseSurvivesRestartAndRelease`
- I-LEASE-3: stale release only when the holder is proven gone; a live owner is never released — `TestStaleReleaseOnlyForGoneHolders`, `TestStaleReleaseRejectsLiveOwnerAndDeletedDirectoryHolder`
- I-LEASE-4: legacy leases fail closed and never auto-enrol into recovery — `TestLegacyLeaseCannotAutoEnrollIntoRecovery`, `TestLegacyHeartbeatFailsClosedAndStatusNeverReclaims`
- I-LEASE-5: resume fences the old token and preserves dirty work — `TestResumeFencesOldTokenReplaysAndPreservesDirtyWork`
- I-LEASE-6: sibling worktrees hold independent leases — `TestIndependentSiblingWorktreeLeases`

## Executor permit (generation fencing)

States: MINTED(generation) → LAUNCHED → EXITED → FENCED → RETIRED.

- I-PERMIT-1: one permit never admits two concurrent writers — `TestOnePermitNeverAdmitsTwoConcurrentWriters`
- I-PERMIT-2: a generation mints exactly one permit; ABA and stale generations are rejected — `TestGenerationCannotMintSecondPermit`, `TestPermitABAAndGenerationAreRejected`
- I-PERMIT-3: a pending launch keeps the fence after restart — `TestPendingLaunchPreventsFenceAfterRestart`
- I-PERMIT-4: launch identity cannot move to another permit — `TestLaunchIdentityCannotMoveToAnotherPermit`

## Integration claim (protected main)

States: QUEUED → CLAIMED(generation) → DISPATCHED → EFFECT(CAS) → COMPLETED | FAILED(no effect) | UNKNOWN(HOLD).

- I-INT-1: dispatch is persisted before any external start and never replayed — `TestIntegrationDispatchPersistsBeforeExternalStartAndNeverReplays`
- I-INT-2: protected effect is a CAS on the target; replay is idempotent per generation — `TestProtectedEffectCASCompletionReplayAndGeneration`, `TestFastForwardCASRejectsTargetChangeAfterPlanning`
- I-INT-3: UNKNOWN before the target keeps HOLD — `TestProtectedEffectUnknownBeforeTargetKeepsHold`
- I-INT-4: a pending launch never releases the claim — `TestIntegrationSupervisorPendingLaunchNeverReleasesClaim`
- I-INT-5: abort proves no effect from physical evidence — `TestProtectedAbortNoEffectPhysicalEvidence`

## Job (native process)

States: QUEUED → RUNNING → DONE | FAILED | CANCELLED | UNKNOWN(after restart).

- I-JOB-1: heavy jobs queue before execution and a cancelled queued job never runs — `TestHeavyJobsQueueBeforeExecutionBudget`, `TestCancelQueuedHeavyJobNeverExecutes`
- I-JOB-2: a job interrupted by restart is UNKNOWN, never replayed — `TestLoadPreservesInterruptedJobUnknown`, `TestExternalLostStartDurableRestartNoReplay`
- I-JOB-3: termination cleans the whole process group — `TestTerminateJobHardCleansProcessGroupAfterLeaderExit`, `TestReapPreservesExitedJobWithLiveDescendant`
- I-JOB-4: recovery proves resources gone before release; legacy jobs only when finished before boot — `TestRecoveryResourcesGoneLegacyJobBeforeBoot`
- I-JOB-5: supervisor crash yields UNKNOWN without a duplicate — `TestP6ChaosSupervisorCrashUnknownNoDuplicate`

## Idempotency

- I-IDEM-1: retry keys survive restart and return the original receipt — `TestSQLiteIdempotencySurvivesRestartWithoutJSONMirror`

## Release retention

- I-REL-1: cleanup keeps the current release, its rollback and uninstalled evidence — `TestReleaseCleanupOnlyCurrentAndRollback`, `TestReleaseCleanupProtectsUninstalledEvidenceBUG82`, `TestRetainedReleaseVersionsBUG109`
