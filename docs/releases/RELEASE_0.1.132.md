# 0.1.132 — Administrative operations in the hardened runtime

Administrative task authorization now transports the exact staged bytes through
the authenticated broker request, using the same bounded base64 chunks as apply.
The broker verifies the staged SHA, the current live SHA and the active
administrative session before recording a permit. It no longer tries to read a
service-owned private staging file. Staging permissions and the permanent broker
capability set remain unchanged; authorization does not apply or reload HA.

Deployment retirement runs the installed gate in a fixed transient systemd unit.
The unit has a deterministic request identity, a 45-second runtime limit,
128 MiB memory limit, no network, and a restricted capability set. The database
main file is mounted read-only; its private parent permits SQLite's service-owner
process to create WAL sidecars. UID transition requires ambient CAP_SETUID in
the worker; SQLite drops to the database owner with zero effective and ambient
capabilities. The permanent broker receives no additional capabilities or mounts.
The fixed broker worker invokes the gate without placing its executable path in
the waiting systemd launcher arguments, preserving legacy-launcher detection.
The gate rechecks the active grant under its lock before effects, preserves the
original deployment receipt and writes the existing permanent execution fence.

Install the reviewed matching broker before using frontend 132 authorization.
Older callers without inline content fail closed; older brokers reject the new
request before effects. Lost replies and worker failures remain UNKNOWN: inspect
the deterministic unit, administrative session and deployment fence instead of
replaying a mutation. Rollback must retain a fence-aware gate and guarded helper.

This release fixes execution boundaries. Native archival, direct iPhone push and
creation of new Codex sessions remain separate acceptance items.
