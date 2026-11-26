# MCPshka 0.2.3 — HomeKit Bridge options flow

Date: 2026-10-26.

## Fixed
- Incremental Zigbee2MQTT export to an existing HomeKit Bridge now handles Home Assistant's extra `bridged_device_triggers` options-flow form after the `include` entity selection.
- The existing trigger settings are submitted with their current defaults; other integration options are preserved. Unexpected steps still fail closed.
- No physical Zigbee commands are issued as part of exposure synchronization.
- `ha_exposure_sync targets=["homekit"]` no longer refuses the whole plan because a YAML-managed (LG TV HA) or single-accessory bridge of the same domain cannot be read: such bridges are skipped with a warning and never saved. An unreadable bridge named by its entry id still refuses.
- Root broker version is now 0.2.3, matching the controller (GitVerse run 1926677 failed `TestHealthBrokerVersionIsControllerVersion`: broker 0.2.2 != controller 0.2.3). The release guide names both constants.

## CI observability
- `forge_ci provider=gitverse mode=status` on a failed run now returns `failure.jobs[]`: job status, failed step names and a bounded excerpt of failed job logs (test failures, file:line, `##[error]`, exit codes). The excerpt is redacted (API token, bearer/token/password values, URL credentials, long opaque strings), at most 2 jobs × 20 lines × 160 chars, and fits the forge_ci output budget. Diagnosis errors are reported as codes and never change the HOLD/observed_pass state; it never authorizes deployment.
- GitVerse workflow: the git-core PPA step retries Launchpad three times and then fails by name when Git 2.44+ (`--no-lazy-fetch`) is unavailable, instead of silently falling back to Git 2.43 and failing executor snapshot tests with exit 129 (run 1927464). Long Go test names are no longer redacted in the excerpt.
- Review fixes (independent Codex review): composite `Authorization: Bearer …`/`Basic` values and job/step names are redacted; the serialized diagnosis has a 10 KiB byte budget (oldest excerpt lines dropped, `excerpt_trimmed`), and logs longer than the 4 MiB scan are marked `log_truncated`.
- `ha_exposure_sync`: the plan digest also covers each target's current list (a same-size change after review refuses apply), and the apply backup holds only the planned targets, so a later restore never rewrites exports that were not selected (Alice when only HomeKit is chosen).

## Known
- A full local sealed module test can end `EXITED_UNFENCED` exit 141, most likely (Codex diagnosis, not yet reproduced) because the strict 8 KiB evidence collector in `internal/executor/systemd_fence.go` returns a write error and the suite gets SIGPIPE. Release evidence is the GitVerse suite; the collector fix is a separate item.

## Validation
- `go test -p 1 ./internal/haconfig` passed on the isolated forge worktree.
- The test fixture now requires the additional HomeKit options-flow step and verifies that existing trigger defaults are retained.
- External immutable-snapshot CI and guarded deployment are required before promotion to production.

## Scope
- `internal/haconfig/flow.go`, `internal/haconfig/sync.go`
- `internal/haconfig/haconfig_test.go`
- `internal/mcpserver/server.go`
- `internal/rootbroker/health.go`
- `.gitverse/workflows/snapshot-contracts.yml`
- `packaging/gitverse/ci_status.py`, `packaging/gitverse/test_ci_status.py`
- `internal/agentguide/guide/release.md`
- `docs/releases/RELEASE_0.2.3.md`
