# MCPshka 0.1.155

Paired controller + broker fix release on top of 0.1.154. The first live
integration run of 0.1.154 found two sandbox plumbing defects; this release
removes them and makes Face ID and heavy work friendlier to the owner and H96.

## Fixes

- **Whole-module test could not start (BUG-20261021-5).** bwrap, already
  running as the guest UID, received the scratch directory as an fd whose path
  lies under the root-only executor authority: `Can't find source path
  /proc/self/fd/4: Permission denied`. PID1 now binds the permit scratch at
  `/run/mcpshka-executor-scratch`, exactly like the workspace. `TMPDIR` is the
  scratch root, so a cleaned scratch still has a valid temp directory.
- **Root preparation units could not reach `/root/repos` (BUG-20261021-3).**
  `ProtectHome=tmpfs` mounts `/root` as 0700, so Git under the service UID was
  refused and preparation held. `integration-test-prepare`,
  `integration-approve` and recipe preparation use
  `TemporaryFileSystem=/root:mode=0711 /home /run/user` with the same
  read-only binds: an empty, traversable `/root` showing only the bound paths.
- **Face ID page says what it is for (BUG-20261021-2).** `admin_session_begin`
  accepts `purpose` (up to 300 characters) which the approval page shows first,
  e.g. «Влить 0.1.155 в main: 3 коммита, CI 7/7». The outdated «no expiry»
  text now states the real window: until the task is closed, at most 24 hours.

## H96 resource discipline

- Whole-module test units run with `Nice=19`, `IOSchedulingClass=idle` and
  `CPUWeight=1`: production services keep CPU and disk; the test waits.
- The free-space headroom for a whole-module test is 2 GiB (was 4 GiB) on top
  of the existing host reserve.

## Operations

- Host Caddy for the Face ID portal now starts after Docker and restarts on
  failure (`/etc/systemd/system/caddy.service.d/10-after-docker.conf`); it had
  been down since boot (BUG-20261021-1).
