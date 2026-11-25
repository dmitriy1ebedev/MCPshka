# MCPshka 0.2.8 — sealed test preparation no longer races the broker RPC

Date: 2026-10-27.

## Fixes
- BUG-149b: the sealed `project_test` constructor (`prepare-recipe`) is scheduled with `systemd-run --no-block` instead of holding the 30 s broker request open, so a full Go tree with its module dependencies can seal within its own bounded unit (RuntimeMaxSec 95 s, worker 90 s, unchanged MemoryMax 256M, CPUQuota 100%, TasksMax 16, private network and capability boundaries). The broker answers `PREPARING`/`SEALED` with the permit and launch identity; the controller launches only after the sealed manifest is observed through the existing one-use launch and fence path. A new read-only `recipe-status` action observes the derived unit; it never schedules or launches. A lost scheduling reply is held as UNKNOWN (`RECIPE_SCHEDULE_UNVERIFIED`), never replayed.
- Status re-checks the seal when the successful transient unit is briefly inactive or already collected, so a finished preparation is not reported absent or failed.
- The controller's preparation window grows from 2 to 3 minutes so it outlasts the scheduling RPC plus the unit's 95 s runtime and 5 s stop; abandon proof is attempted only after it.

## Measurement harness (no runtime change)
- `BenchmarkBrokerNormalRoundTrip` and `BenchmarkBrokerCancelledRoundTrip` measure the broker Unix socket client on a synthetic peer. A cancellation hook evaluated with them was rejected (+41.7% normal-path latency on the synthetic fixture) and is not part of this release.

## Not claimed
- No end-to-end tunnel latency or throughput improvement is claimed by this release. Live async preparation is verified only after installation.

## Rollback
- Previous pair 0.2.7 (controller/broker) is retained by coordinated upgrade as `.previous`.
