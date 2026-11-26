# MCPshka 0.1.158

Broker fixes on top of 0.1.157. The fourth live integration run passed the
whole-module test and received its approval, then stopped at
`integration_worker_launch` (BUG-20261021-12).

## Fixes

- **The broker sees the protected integration store.** `mcpshka-broker.service`
  had `ProtectHome=true`, so the in-broker integration actions
  (approval status, worker launch/status/fence) could not reach
  `/root/repos/mcpshka-controlled` and held. Approval issuance worked only
  because it runs in its own root unit. The unit now uses `ProtectHome=tmpfs`
  with `BindReadOnlyPaths=-/root/repos/mcpshka-controlled`: the rest of `/root`
  stays hidden and the store is read-only to the broker; integration workers
  keep their own read-write unit. A packaging test pins both lines and forbids
  `ProtectHome=true`, `BindPaths=` and `ReadWritePaths=/root`.
- **The integration worker can read the source repository
  (BUG-20261021-13).** The worker unit combines `NoNewPrivileges` with
  seccomp-backed sandboxing, under which systemd leaves `CAP_SETUID` out of
  root's effective set (`CapEff=0x62`). The object import reads the source
  repository as the service user, so `setresuid` failed, git never started and
  the import stopped at `IMPORT_INTENT` with no objects. The unit now sets
  `AmbientCapabilities=CAP_SETUID CAP_SETGID` like every other root unit
  (`CapEff=0xe2`); the bounding set is unchanged.

Verified live before release: under the old sandbox `integration-status`
holds; under the new one it returns the approval. A unit with the worker's
properties fails `setresuid` to UID 994 and succeeds with the ambient set.
