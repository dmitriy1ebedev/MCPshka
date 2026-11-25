# One code per privileged task (Apple Passwords TOTP)

**State:** implementation pending controlled release and root-broker installation.
This feature does NOT switch the MCPshka HTTP service to root, store the
Linux root password, or enable a generic root shell.

## Security boundary

- The authenticated MCP HTTP frontend stays UID `mcpshka`.
- An existing Unix-socket broker authenticates that frontend's *actual service PID*.
- TOTP is verified only by the root-owned broker. The TOTP seed must live at
  `/etc/mcpshka/totp.secret`, be owned by root, mode 0600, and never be supplied
  to ChatGPT.
- A TOTP code submitted once authorizes the **entire pinned task**, not each
  read, file update, rollback, or completion separately. Per-task privileges
  expire in 60 minutes or upon an explicit `close`.
- A permit is bound to the MCP identity, 128-bit task ID, exact scope and
  SHA-256 of the reviewed original and staged content.
- The root-owned ledger prevents TOTP replay and locks verification for 10
  minutes after five failures. Only hashes/short state enter contract audits;
  the six digits are omitted from fingerprints and persisted receipts.
- The root broker is an allowlisted dispatcher; no `sudo`, shell, arbitrary
  executable, or generic directory access becomes available.

### First scope: Home Assistant automation management

`root_task_prepare` calculates the current production/staged hashes.
`root_task_authorize` consumes one Apple Passwords verification code and
creates the time-limited task authorization.
`root_task_execute` supports `apply`, `rollback` or `close` with **no**
subsequent TOTP code. `root_task_status` checks progress without consuming it.

The initial implementation supports ONLY
`/opt/homeassistant/config/automations.yaml`, using staged content already
reviewed in `/var/lib/chatops/ha-bridge/automations.yaml`. It checks both
SHA-256 values again, validates YAML, takes a root-owned backup, and atomically
replaces the production file. Rollback also verifies the exact approved
fingerprints. Reloading automations in Home Assistant remains a separate step;
the operation reports `reload_required`.

Never disable the broker's PID-bound transport or widen its action catalog
to arbitrary commands in response to a correct code.

## One-time enrollment on the H96 (Termius, root only)

Only after the new broker is installed, provision the code generator locally.
Do NOT paste the secret or enrollment URL into chat.

```bash
umask 077
install -d -o root -g root -m 0700 /etc/mcpshka
python3 - <<'PY'
import base64, os, secrets, urllib.parse
path = '/etc/mcpshka/totp.secret'
key = base64.b32encode(secrets.token_bytes(20)).decode().rstrip('=')
try:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
except FileExistsError:
    raise SystemExit('TOTP already enrolled; refusing to replace existing seed')
with os.fdopen(fd, 'w') as f:
    f.write(key + '\n')
    f.flush()
    os.fsync(f.fileno())
print('Add a verification code in Apple Passwords using this secret key:')
print(key)
print('Optional provisioning URI (local terminal only):')
print('otpauth://totp/' + urllib.parse.quote('MCPshka:armbian') +
      '?secret=' + key + '&issuer=MCPshka&algorithm=SHA1&digits=6&period=30')
PY
```

Verify that device clock is synchronized before using TOTP.

## Task flow

1. User asks for a defined operation. The assistant uses `root_task_prepare`
   and explains the affected resources, reviewed change and expected result.
2. Prefer `root_task_begin_passkey`: the owner opens the task-specific HTTPS
   page and confirms with a passkey requiring user verification. The broker
   verifies the assertion and automatically applies the exact approved bytes;
   no additional chat message or execution command is needed.
3. TOTP remains an alternative through `root_task_authorize`. A six-digit
   code can be retained in chat history; never send the enrollment seed.
   Subsequent allowed task actions do not consume another code.
4. Inspect the durable result and actual configuration, then close the task.
   Unexpected changes, expiry or a different identity stop privileged work.

### Versioned approval and upgrade order

Plan version 1 receipts record `auth_method` (`webauthn` or `totp`) and
`authorized_at`. A root-owned current-approval record pins the task ID and
staged content hash. Issuing a newer approval supersedes earlier permits for
this resource, including their rollback permission. A new approval must be
prepared if an older backup needs to be restored. Backups are retained.

Approval issuance, apply and rollback share the same resource lock. The
current approval is checked while holding that lock through replacement.
Historical unversioned receipts remain readable but cannot authorize a new
mutation. Missing or malformed current-approval state fails closed.

Install and verify the compatible broker before deploying the version 1 MCP
frontend. The frontend checks the broker protocol before preparing or starting
a privileged action. The existing service-user installation restriction is
preserved; broker replacement requires the authorized operator upgrade path,
with a pinned candidate, backup, receipt and verified rollback.

The code is not a Linux root password. Other privileged tasks require
explicit audited scope implementations, not free-form root access.
## Concurrency and durability

The Face ID completion path uses the same task execution function as TOTP-scoped
operations. The task permit stays locked through application and receipt saving.
Apply and rollback also share a private, nonblocking resource lock across task IDs;
a busy target returns HOLD. Apply rechecks the approved production hash immediately
before replacement. These locks cover broker participants; independent writers
outside this protocol still require reconciliation.

Approval receipts, TOTP replay state, and passkey records synchronize their parent
directories as well as file contents. The task backup is synchronized before
production replacement. A directory-sync error after replacement is an uncertain
outcome: inspect actual task/config/backup hashes, never blindly repeat the mutation.

Unit coverage verifies competing lock rejection, lock release, unsafe links and
permissions, and exclusive receipt replay. It does not substitute for WebAuthn
owner acceptance, reload verification, a real rollback, or power-loss testing.
