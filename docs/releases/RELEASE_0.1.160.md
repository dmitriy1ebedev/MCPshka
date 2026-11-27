# MCPshka 0.1.160

Integration without host builds, plus the Codex job-tool slice, on top of
0.1.159.

## CI evidence gate (load off the H96)

Every integration rebuilt the whole module on the H96 for 23–50 minutes while
GitHub CI had already run `go test ./...`, race and fuzz on the same commit.
`integration_test_prepare` now defaults to `evidence=ci`:

- A networked root unit (HTTPS only, the root-only token bound read-only, the
  rest of `/root` hidden) reads the commit's check runs, keeps the latest run
  of each of the seven required jobs and requires all `success`. It reads the
  commit tree and checks that the PR base the runs merged with is an ancestor
  of the commit, so the tested tree is the commit's tree (integration is
  fast-forward only).
- The evidence is sealed into a one-file workspace through the shared permit
  preparation; its digest is in the manifest.
- The launch stays offline: the worker re-verifies the sealed file in process
  and records the usual result, which is fenced as before.
- `integration_approve` accepts the new purpose; the protected worker path is
  unchanged. `evidence=module` keeps the host test as a fallback.
- `internal/cievidence` joins the protected core.

## Job tools (Codex, PR #76)

- `project_test` takes optional `packages` and `run` with strict validation
  (BUG-20261020-24).
- `job_status` is compact by default (`verbose=true` for the full snapshot);
  new read-only `job_wait` (≤ 120 s, ≤ 2 KiB of output tails) (BUG-20261020-12).
- The service user's Go build cache is cleaned above 1 GiB, only while no Go
  job runs; bounded and audited (BUG-20261021-4).
- Native `go test`/`go build`, `project_test` and `project_build` run at
  nice 19 with idle I/O.

## Fixes

- **Publish names a locked checkout (BUG-20261021-15).** A leftover
  `.git/index.lock` made `integration_publish_main` fail with only "outcome
  UNKNOWN". A lock younger than 30 minutes is now reported as HOLD with its
  age; an older, abandoned one is cleared (`PUBLISHED_STALE_LOCK_CLEARED`).
- **Fenced executor units no longer stay failed (BUG-20261021-16).** After the
  fence or recovery is durable, the systemd backend resets the unit's failed
  state.
