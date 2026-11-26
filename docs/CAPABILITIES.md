# Capability discovery

MCPshka 0.1.147 adds two read-only discovery tools. Existing tool names and
authorization remain compatible.

1. Call `capabilities_bootstrap` for the running image SHA-256, an authorization
   dependent catalog revision, authorized categories and counts.
2. Call `tool_resolve` with a tool name or a short RU/EN goal and optionally the
   bootstrap `expected_revision`. It recommends up to three existing authorized
   native entrypoints with full schemas and mutation contract metadata.
3. Validate the arguments and use the original tool. Discovery does not execute
   actions, acquire leases, authorize providers or grant approval.

The revision covers the compiled catalog, schemas, descriptions, aliases,
mutation contracts, policy, build, version, broker mode, service UID and normalized
caller role/scopes. A changed identity ID alone does not change permissions or
invalidate the revision. No credentials or policy contents are returned.

`CLIENT_SCHEMA_STALE` returns no candidate actions. Repeat standard MCP
`tools/list` with pagination and obtain a new bootstrap revision. If a ChatGPT
connector snapshot still hides these tools, refresh or enable the connector in
the client. The server cannot force a client to expose a tool or access unrelated
ChatGPT plugins. Initialization instructions provide this discovery fallback.

Server registration, caller permission, client exposure, provider authentication,
backend health and readiness are distinct. Discovery verifies registration and
tool-level permission only; the latter states remain `unknown` until separately
probed. Argument policy, protected-core approval, leases and deployment fences
still apply at the original operation. Known disabled `broker_install` is not
recommended after service-user activation.

Resolver goals are bounded to 256 UTF-8 bytes and structured results to 32 KiB
(the MCP transport envelope can be larger). Search is
deterministic lexical matching, not a semantic planner or calibrated probability.
If a schema exceeds the response budget, `SCHEMA_OUTPUT_LIMIT` directs the client
to standard paginated discovery; schemas are never silently truncated. A missing
authorized match is `NO_AUTHORIZED_MATCH`. Unavailable running-image identity
fails discovery explicitly without disabling existing tools.

This first slice does not claim end-to-end cold-chat discovery, automatic client
refresh, provider readiness, external-plugin discovery, a persisted index or a
multi-step executable workflow. Those require separate integration acceptance.
