# 0.1.139 — Durable Codex session creation and owned loading

The bounded ROADMAP reader now accepts up to 256 KiB, accommodating the updated
operator-supplied roadmap and its pending-work notes. SHA pinning, phase/check
limits and advisory-only acceptance semantics remain enforced; larger documents
are rejected. The installed 128 KiB reader rejected that document before this
compatibility fix.

`codex_session_create`, `codex_session_creation_status` and `codex_session_load`
add a native lifecycle for a new Codex session in `/root/repos/mcpshka`.
The caller supplies its active `admin_session_id`, one stable 32-hex
`creation_key`, and an `idempotency_key` for mutations. Loading additionally
requires a separate 32-hex `load_key`. Reuse the creation key after a lost reply.
The authenticated owner comes from the MCP transport, never the request.

A root-owned immutable intent is written before dispatching one bounded PID1
worker. CLAIMED precedes the daemon mutation; IDENTIFIED preserves the returned
thread ID before checking the remaining metadata and MCP inventory. HELD retains
uncertainty, while READY records successful isolation and its observation time.
Repeated commands inspect this intent; they never create a replacement session.
If the worker dies before it can preserve an ID, the outcome remains UNKNOWN.
Neither age nor an absent worker authorizes repeating creation.

Load can resume only the exact thread identified by this owner's creation
intent. Its independent load intent reuses that ID; no arbitrary thread ID is
accepted. Both creation and loading recheck the administrative grant/generation.
Loading and existing native task submission share one admission lock. Public
READY also waits for the worker to release admission. Status is durable evidence,
with a last observation timestamp; it is not a promise that an idle session
cannot subsequently unload. Existing task submission rechecks the live thread.

Start and resume explicitly request read-only sandboxing and approval policy
`never`, disable the bounded effective inventory of MCP servers, plugins and
apps, and disable default apps. Readiness requires a matching read-only response,
loaded idle thread, complete thread-scoped MCP inventory, every expected server
present and every returned runtime disabled with no tools/resources. Starting,
missing or incomplete evidence remains HOLD. Create/load never start a model
turn or queue a prompt. The returned `thread_id` is the target accepted by the
existing `codex_task_submit` and `codex_session_read` APIs; `codex_session_id`
separately preserves the daemon's session-tree identity.

The client verifies root-private socket provenance, the kernel's root peer
identity and its executable inode against the selected installed CLI. The
managed CLI image may belong to its installer; this is not proof of a root-owned
image. A replaced CLI inode fails closed. The fixed client is embedded in the
broker, so ordinary paired releases update it without a helper bootstrap.
Its read-only namespace retains host `/tmp` because the authenticated socket
resolves there. Only executor state is writable. Temporary CAP_SYS_PTRACE is
needed to inspect the privileged daemon's `/proc/PID/exe`; no caller program,
path, configuration override or arbitrary daemon method is exposed.

Cached MCP schemas can use
`exec_start(command="mcpshka", args=["codex-session", JSON], idempotency_key=KEY)`.
JSON fields are `action` (`create`, `status`, `load`), `admin_session_id`,
`creation_key` and optional `load_key`. The bridge checks target scope and owner
before cached receipts. Read its completed job output for lifecycle status, and
use a fresh status retry key for a new observation. This route runs no caller
executable. It does not grant writer authority or bypass existing leases.

Acceptance requires all five pinned cloud gates, paired deployment, a real
create/status/repeat/load cycle, live session read-back and safe handoff evidence.
Writer admission, iPhone push and the full original reliability audit remain
required work beyond this slice.
