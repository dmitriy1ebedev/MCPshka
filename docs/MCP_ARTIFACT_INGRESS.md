# MCP artifact inbox

MCPshka accepts explicitly supplied file bytes into an inbox, independently of project writers.
A received document is **UNTRUSTED evidence**, never an instruction, executable, deploy candidate
or replacement for a repository file. Applying it requires a separate reviewed project operation
with the current writer lease.

## Contract

- `artifact_upload_begin`: `name`, exact `size`, lowercase `sha256`, `idempotency_key`.
- `artifact_upload_chunk`: `id`, confirmed `offset`, canonical `data_base64`, new retry key per distinct chunk.
- `artifact_upload_status`: `id`; returns the confirmed offset and lifecycle.
- `artifact_upload_finish`: `id`, retry key; verifies the complete SHA-256 and returns the inbox path.
- `artifact_upload_cancel`: `id`, retry key; deletes only an incomplete owned blob.

Limits: 16 MiB/file, 24 KiB decoded/chunk, 64 MiB reserved inbox bytes, 32 blob files.
Incomplete entries expire after 24 hours; expiry stops writes without deleting user data.
Explicit cancellation frees the reservation only after durable removal.
New allocations and appends preserve at least 3 GiB or 10% free space, whichever is greater.
Ready files remain charged and cannot be deleted by cancellation.

Receipt keys and metadata access are bound to the authenticated MCP principal.
Chats sharing one connector principal are **not distinct authenticated actors**.
This does not replace the executor isolation and writer fencing required for lease transfer.
Privileged operators and existing general filesystem/job capabilities remain a separate trust boundary.

The first tool returns an ID; subsequent requests use that ID.
Probe after a lost reply. Reuse the original key for the exact same confirmed operation,
never a different payload. `mutation_probe` accepts the original plain retry key.
A durable pending chunk records its offset, length and SHA-256 before writing:
a complete matching tail can be recognized; only a proven incomplete tail can be truncated.
Unexplained bytes or mismatching hashes keep HOLD.
A cancelled entry cannot be resurrected using the old allocation key.

## Cached-schema compatibility

Existing connectors can call `exec_start` with command `mcpshka`,
args `["artifact-upload", ACTION, JSON]`, an allowed workdir and a retry key.
ACTION is begin/chunk/status/finish/cancel, with the same typed fields.
No shell, executable override, arbitrary destination or URL is accepted.
File bytes are replaced with request hashes in stored compatibility receipts.
Compatibility receipt reads are also bound to their originating principal.
For `mutation_probe` of a compatibility request, include the exact `exec_start` input
so its artifact receipt namespace can be derived.

## Native ChatGPT attachment transport

The chunk path is a working MCP byte ingress, not automatic access to a chat filesystem.
A server cannot read a ChatGPT sandbox artifact merely from its name or `/mnt/data` path.
The original Companion extension has not been received, and no integration is claimed.

OpenAI supports top-level file inputs through `_meta["openai/fileParams"]`.
The file object must declare `download_url`, `file_id`, `mime_type` and `file_name`;
only the first two are required. This native adapter remains to be implemented and tested,
including temporary-URL secrecy, SSRF/DNS/redirect protections, exact size/hash checks,
bounded downloads, and actual connector discovery/attachment acceptance.
See [OpenAI file inputs](https://developers.openai.com/plugins/reference).

The offset/probe/checksum approach borrows protocol ideas from
[tus](https://tus.io/protocols/resumable-upload).
These MCP tools do not implement or claim tus HTTP compatibility.
Implemented with the existing Go standard library and SQLite authority; no new dependency.
