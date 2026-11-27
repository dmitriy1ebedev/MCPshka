# 0.1.129 — revocable administrative work session

One purpose-specific WebAuthn approval opens an owner-bound administrative work
session. The grant survives MCPshka/broker restarts and lost tunnels and has no
mandatory wall-clock expiry. The five-minute login challenge and bounded job
timeouts remain separate. Explicit `admin_session_end` with `close` or `revoke`
ends the grant and advances its durable generation. Already admitted effects
serialize with revocation; a lost response requires a status probe.
Pending challenges can also be cancelled before Face ID; their durable
cancellation marker cannot create authority or revoke another active grant.

`admin_session_prepare`, `admin_session_begin` and `admin_session_status` expose
the same workflow to authenticated MCP clients. An already active session is
reused only by its owner. Anonymous/local-stdio contexts cannot acquire it;
owner identity participates in the mutation receipt fingerprint. Neither an old
HA approval nor an old generation's challenge can authorize a new session.

`admin_session_authorize_task` binds prepared HA hashes to the current session;
the existing root-task apply/rollback/close tools check its live generation.
`admin_session_retire_deployment` invokes the fixed 128 retirement gate with
exact deployment/receipt/current-binary pins. No arbitrary shell or caller path
is accepted. Operation allowlists, release gates, leases, backups and UNKNOWN
reconciliation remain mandatory. The session is general administrative consent;
individual capabilities still require an implemented and reviewed broker route.

The session state and transition audit live in the private root executor
directory. Corrupt, foreign, superseded or unsafe records keep HOLD. Status
preserves historical task outcomes while separately reporting whether their
authorization is active or blocked.
If task authorization was interrupted before publishing its approval head,
reconciliation reports HOLD rather than restoring an older/superseded head.
Inspect the unchanged HA hashes and prepare a new task; retain the original
UNKNOWN mutation receipt. Authorization itself never applies HA content.

The portal displays the administrative purpose separately from legacy HA-only
confirmation. The deployed 109 broker cannot issue this authority. Production
activation requires a reviewed compatible privileged installation and a real
new-purpose Face ID approval; publishing or deploying the MCP binary alone does
not perform that bootstrap or clear a stale deployment.
