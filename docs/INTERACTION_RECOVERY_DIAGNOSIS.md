# Bounded interaction recovery diagnosis

`interaction_recovery_diagnose` accepts a canonical project path, optional job ID
and an optional client-reported error (2048 bytes). Cached clients can use
`exec_start` with command `mcpshka` and arguments
`["recovery-diagnose", JSON]`, a fresh idempotency key and an allowed workdir.

The tool observes the lease, bounded project jobs, workflow claim, pending
deployment and original failed-job output. A workflow claim remains relevant
even when no job is running: the response includes its ID/state and
WORKFLOW_ACTIVE guidance to retain HOLD. It does not prove the owner has died.

Each finding distinguishes observations from inferences from untrusted output.
The result contains output hashes, typed recovery steps and at most two visible
Knowledge Core case IDs, never raw logs or the client report. Similar cases
are evidence to inspect, not automatic authority. Lookup uses an ephemeral
index and does not write persistent index health.

Diagnosis never renews/releases/transfers a lease, grants approval, launches a
test or enables replay. retry_allowed is always false. Correcting a confirmed
failed test requires a separate authorized action with a new retry key; unknown
effects must first be reconciled. Permission failures and missing config files
are distinct DIAG regressions; private test fixtures must avoid production
secrets.

The compatibility receipt is principal-scoped, has sanitized arguments and
owner checks on job_status/job_output. The common mutation-probe key locates
that receipt without executing diagnosis again. It is not an executor fence,
an administrative transfer or a complete automatic recovery workflow.
