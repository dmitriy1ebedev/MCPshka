# OpenAI Secure MCP Tunnel for MCPshka

This is the supported outbound-only path for connecting the local MCPshka MCP server to OpenAI without exposing port 8765 to the public Internet.

## Local layout

- MCPshka MCP: `http://127.0.0.1:8765/mcp`
- tunnel-client binary: `/usr/local/bin/tunnel-client`
- pinned upstream release: `openai/tunnel-client v0.0.15`
- tunnel profile: `/etc/mcpshka/tunnel-client.yaml`
- runtime credentials: `/etc/mcpshka/tunnel.env`
- local MCP Authorization header: `/etc/mcpshka/mcp-auth-header`
- MCPshka hashed identity registry: `/etc/mcpshka/auth.json`
- service: `mcpshka-tunnel.service`
- health/admin UI: `127.0.0.1:8768`

The local MCP bearer token is not embedded in YAML. tunnel-client reads the complete Authorization header from a root-only file. MCPshka itself authenticates that bearer through `auth.json`, which stores only a credential identity and SHA-256 digest. Registry entries support overlap during rotation plus immediate revocation/expiry without restarting MCPshka.

During migration from pre-registry releases, `MCPSHKA_TOKEN` may remain temporarily in the server environment only to preserve rollback to the previous binary. Once the rollback release is registry-aware, the plaintext environment copy can be removed; the tunnel still keeps its root-only Authorization header.

## Provisioning boundary

The local host can be prepared completely without OpenAI credentials.

Two values must then be created in OpenAI Platform:

- `CONTROL_PLANE_TUNNEL_ID=tunnel_...`
- `CONTROL_PLANE_API_KEY=...`

The long-lived runtime key needs Tunnels Read + Use. Do not use an admin key as the daemon runtime key.

Put both values into `/etc/mcpshka/tunnel.env`, keep that file mode 0600, and never commit it.

## Activation

Validate first:

    tunnel-client doctor --config /etc/mcpshka/tunnel-client.yaml --explain

Then:

    systemctl enable --now mcpshka-tunnel.service
    systemctl status mcpshka-tunnel.service --no-pager
    curl -fsS http://127.0.0.1:8768/readyz

Only after the tunnel reports ready should the ChatGPT connector be created or verified.

## Security model

MCPshka itself remains bound to loopback. The tunnel-client initiates outbound HTTPS to the OpenAI control plane. It authenticates locally to MCPshka with the same bearer policy as any other HTTP MCP client.

The tunnel service is separately sandboxed by systemd and has no filesystem write access outside its own state directory.

## Final rename

The legacy Remoutka identity is migrated to MCPshka with an explicit production upgrade/rollback test; new installations use only MCPshka names and paths.

## Transport failures and truthful readiness

A local `readyz` response proves local tunnel readiness, not availability of the remote OpenAI control plane. On 2026-10-15, authenticated loopback MCP `project_context` succeeded while the chat connector returned generic `-32603 Internal error`; the tunnel journal showed upstream503/504 and `tunnel_transport_unavailable`. Classify this as degraded external transport, not a successful remote management-path check.

Inspect bounded recent tunnel logs and local authenticated MCP/health before changing anything. The tunnel already backs off and reconnects; an upstream5xx alone does not justify rotating credentials, disabling auth, changing networking or restarting healthy services. Mutating calls with lost responses remain UNKNOWN and require their original receipt/job/deployment probes before retry. Read-only probes may be retried with bounded backoff.

When an independently authorized local recovery channel exists, use the same existing MCP HTTP identity on loopback for bounded diagnosis/work; do not expose its bearer, create a second identity, or bypass roles/policy. Verify actual chat connector calls again after the remote transport recovers. A local fallback does not prove chat transport recovery.
