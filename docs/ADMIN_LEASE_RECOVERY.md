# Administrative main-repository lease recovery

The authenticated `project_lease_admin_recovery_status`, preview and recover
operations use a short-lived PID1 worker. The broker retains its existing
ProtectHome/ProtectSystem paths and capabilities. The fixed worker exposes only
`/root/repos/mcpshka` read-only from home and writable MCPshka SQLite/session
state, with a private network, 45-second lifetime and bounded CPU/RAM/tasks.
It runs the installed broker executable; callers cannot choose paths, commands,
unit properties or successor authority. Response stdout is piped, not journaled;
the successor holder token must never enter logs.

The worker rechecks the active owner-bound Face ID grant under the session lock.
Recovery retains the existing exact lease/generation/execution/record SHA and
Git-tree pins, root-owned sealed directory checks and host process/reference
scan. It refuses dirty work, changed holders, active deploys, live writer
references and bound owners. Preview does not seal directories or grant transfer. Before Git status it requires
the sealed/inert scope; executable clean/process filters and gitlinks produce HOLD.
An operator must supply the existing verified OS fence; the worker never changes
repository permissions, kills an owner or removes a lock.

The existing SQLite transaction rotates ownership and writes an immutable
recovery journal atomically. Old tokens cease to authorize writes; writer
admission stays closed until the verified successor opens its pinned gate.
A timeout/lost result remains UNKNOWN: inspect current holder and journal;
never resubmit recovery with a fresh retry key. Read-only status/preview remain
available for reconciliation. This release fixes administrative resource access;
it does not weaken fencing or infer owner death from elapsed time.
