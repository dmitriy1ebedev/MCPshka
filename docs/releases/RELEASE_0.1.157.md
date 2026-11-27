# MCPshka 0.1.157

Controller + broker fix release on top of 0.1.156. The third live integration
run finished the whole-module test in 21 minutes: 37 packages passed and 4
failed only because of the sandbox snapshot (BUG-20261021-11).

## Fixes

- **The module snapshot includes `packaging/`.** `protectedcore` and
  `releaseprep` tests read the committed systemd units and root gate scripts.
  Committed executable helpers (`100755`) are accepted only under
  `packaging/` and are materialized read-only `0444` like every snapshot file.
- **The live network test skips an isolated namespace.** The sandbox has
  links but no listeners or resolver, a valid host state; the test now skips
  instead of failing.
- **No TempDir race in `TestCandidateCleanupAdmissionCanonicalJobAndLease`.**
  Cleanup waits for the cancelled job's final persist (`Manager.Close`)
  before the job directory is removed.

## Next

First pipeline run of this commit into `main`: `integration_enqueue` →
`integration_test_prepare` → launch/status/canary-fence →
`integration_approve` → `integration_worker_launch` → `integration_publish_main`.
