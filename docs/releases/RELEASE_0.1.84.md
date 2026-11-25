# MCPshka 0.1.84 — administrative lease review foundation

This release integrates the six commits from chatgpt/admin-lease-transfer-20261009,
preserving authorship and history. Administrative review is read-only and is also
available to cached clients through exec_start command mcpshka, args
["lease-review", PROJECT_PATH].

Review reports UNKNOWN, SUSPECT or RELEASED and an explicit fencing state.
Stale heartbeat is a suspicion, not evidence that an owner is dead. Process
binding metadata is not a fresh liveness proof. The review does not issue approval,
release ownership or accept a deployment outcome.

The credential-rotation primitive is closed behind an in-package writer-fence
gate. No trusted fence controller is installed in this release, so administrative
transfer remains HOLD. Caller assertions about idleness cannot enable it.
This fixes the prototype's misleading assumption that rotating a holder token
also revokes filesystem or process write rights.

Validation: full Go suite passed through bounded MCPshka execution; independent
review found no blockers. Production acceptance is recorded separately only
after guarded deploy, reconnect and smoke. Universal lease reconciliation,
authenticated session fencing, generation-based transfer, one-time approval and
DIAG P02 completion remain unfinished. No protected-core helpers are installed.

Codex lifecycle hooks are registered locally, all six definitions are trusted
and active in the installed CLI. The handler was executed in a controlled check.
Hooks remain hints, not authoritative session liveness or automatic lease release.
