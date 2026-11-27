# 0.1.101 — task-scoped TOTP approval (Apple Passwords)

The previous 0.1.100 Docker control plane remains unchanged.

Added broker-mediated one-code-per-task privilege grants for the initial
`homeassistant_automations` scope. The MCP server remains the unprivileged
`mcpshka` service; a correct code does **not** launch unrestricted root or
suppress operation-level validation.

- `root_task_prepare` returns a new task ID and pinned production/staged hashes.
- `root_task_authorize` accepts one six-digit TOTP code and issues a 60-minute
  owner-bound grant for the reviewed plan.
- `root_task_execute` supports `apply`, `rollback`, `close` without another
  OTP. `root_task_status` exposes expiry and state.
- Root broker checks code/replay/5-attempt lockout against an owner-validated
  root-only seed and persistent replay ledger. The six-digit value is excluded
  from durable MCP mutation fingerprints and audit evidence.
- HA modifications are restricted to exactly `automations.yaml`. Production
  and staged hashes are pinned; YAML is validated; backup/atomic write are
  performed by the root broker. Rollback is allowed only when the production
  fingerprint still matches the approved applied content. HA automation reload
  is not yet automatic.

Availability requires a **separate approved broker installation** after the
guarded frontend deployment, plus one-time local enrollment of Apple Passwords.
Never give the permanent enrollment secret or Linux root password to ChatGPT.
See [ROOT_TASK_TOTP.md](ROOT_TASK_TOTP.md).

Risks and non-goals: NO arbitrary privileged shell, no privilege increase to
the MCP service account, no arbitrary files or Docker exec. The broker's
`ReadWritePaths` is extended only to the Home Assistant config mount and its
code enforces a single fixed output file. The initial staging transport limits
HA YAML to 10 KiB, due to the authenticated broker request envelope. Production
mutation must not be attempted until broker installation and end-to-end checks.

Release acceptance: Go build/test, rootbroker TOTP vectors and policy tests,
candidate identity, guarded deployment, broker installation by trusted local
administrator, and live `root_task_prepare` plus negative security probes.
