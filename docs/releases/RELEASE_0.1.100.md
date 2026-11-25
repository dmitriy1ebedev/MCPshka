# 0.1.100 — Docker control plane

Release candidate; production acceptance is recorded separately.

Persistent explicit Docker read scopes enable list/status/inspect/logs/stats for
the approved home services. Missing configuration returns an actionable HOLD.
The Docker socket remains exclusive to the privileged broker; jobs do not gain
Docker-group membership.

Three tools implement interactive Engine exec/TTY sessions: open, IO and status.
Live terminal acceptance on H96 is BLOCKED: its kernel lacks Landlock. The
production terminal allowlist remains empty; interactive access is not enabled.
Sessions have authenticated owners, immutable container identities, byte cursors,
bounded resources, durable dispatch intents and idempotent input. Default shells
are non-root and filesystem-read-only. Closing requests shutdown; only confirmed
supervisor completion releases a writer slot. Lost outcomes remain UNKNOWN/HOLD.

The supervisor is a separately built protected component. The broker verifies
its actual executed hash and kernel credentials before allowing input. Broker
CAP_SYS_PTRACE and the narrow container helper capabilities require the existing
administrative installation gate. See [Docker access](DOCKER_ACCESS.md).

This release does not complete the remaining R1 linked-worktree/job-isolation
acceptance or the Codex Control Plane. HA automation contents are not changed.
