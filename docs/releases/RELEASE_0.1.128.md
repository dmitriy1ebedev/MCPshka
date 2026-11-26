# 0.1.128 — permanent deployment fences

A stale `accepted` receipt remains UNKNOWN. A separate, root-owned fence can
exclude its deployment ID from future execution without inventing a terminal
outcome or permitting a retry. Reconciliation reports `blocking`, `fenced`,
`fence_sha256` and `fenced_count`; original receipt bytes remain authoritative.
The native installer now installs the Python gate and guarded shell helper at
their distinct production paths instead of replacing the gate with the shell.

Retirement is deliberately unavailable until a compatible privileged gate and
helper are installed and an owner has opened a WebAuthn administrative session.
The old Home Assistant approval does not authorize release maintenance.

The administrative session consumer expects the root-owned
`/var/lib/mcpshka-executor/admin-session-current.json` schema
`mcpshka.admin-session/v1`, policy `mcpshka-administration/v1`, an active state,
positive generation, authenticated owner, WebAuthn method and authorization time.
It imposes no wall-clock lifetime on that grant. Challenge expiry and bounded
operation timeouts are separate. The session issuer and revocation API are the
next release slice; this release cannot create that authority itself.

The installed root gate's `retire-deployment` command requires the exact
deployment ID, original receipt SHA-256, current binary SHA-256, reason, session
ID and owner. It serializes with session revocation, release admission and
deployment execution. It verifies the unit is absent and the installed pair
supports fence admission. Unknown or conflicting evidence keeps HOLD.

Before recording a fence, the gate rejects live legacy launchers and disables
execution of fence-unaware sealed helpers under the private release authority.
Their bytes and hashes are preserved. Other nonterminal legacy deployments keep
HOLD. A delayed queued direct `execve`/systemd `ExecStart` cannot execute those
old paths after this barrier. Their execute bits must never be restored;
rollback materials are retained, but old one-use launch entry points are retired.
No production permission or service is changed merely by installing this source.

The new helper checks its deployment fence under the deployment lock before any
status write or binary swap. Fence lookup failures and lock conflicts preserve
the original receipt. Admission checks the fence again under the release lock.

Fence observations bind the receipt and installed gate/helper hashes. Updating
either privileged component intentionally restores HOLD until its fence trust
is reconciled through a reviewed privileged migration. A binary-only MCPshka
update does not change these pins. Never clear fences by age, replay the original
deployment, or rewrite an UNKNOWN receipt to manufacture an idle project.

Verification includes strict/malformed fence evidence, foreign or revoked
sessions, long-lived grants, absent units, repeat retirement, an old sealed
launcher delayed before dispatch, direct execution after its execute bits are
removed, helper lock conflicts and admission before effects. Cloud Go checks
remain mandatory; production retirement and physical Face ID acceptance remain
separate live gates.

The fuzz suite uses Go 1.27.2 to include the upstream fix for Go #75804, a
coordinator cancellation race that can report a normal fuzztime expiry as a
test failure. Its 30-second target budgets, worker limits and failure handling
are unchanged. Other suites retain the module-selected release toolchain.
