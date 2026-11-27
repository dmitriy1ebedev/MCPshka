# Client compatibility

Last verified: 2026-10-06.

## Claude Free

Claude Free can be a MCPshka client without using the Anthropic API.

Anthropic currently allows Free users to configure one custom remote MCP connector. Remote connectors are available from Claude clients including web and mobile, and the connection originates from Anthropic's cloud infrastructure.

This is different from embedding Claude inside MCPshka:

- Claude Free chat can call MCPshka MCP tools through a custom connector.
- Claude Free does not provide a free general-purpose Anthropic API entitlement for MCPshka to call programmatically.
- Claude Code is not included in the Free plan according to Anthropic's current desktop plan table.

### Required MCPshka path

    Claude Free
        |
        | remote MCP / HTTPS
        v
    public authenticated endpoint
        |
        | outbound relay / reverse tunnel
        v
    127.0.0.1:8765
        |
        v
    MCPshka

MCPshka itself should remain bound to loopback. The public edge must terminate TLS and forward only authenticated MCP traffic. Do not expose the MCPshka listener directly on 0.0.0.0.

Claude custom connectors can send fixed request headers, including bearer/API-key style credentials. MCPshka 0.1.a.3 implements bearer authentication on its local Streamable HTTP endpoint. OAuth remains a later hardening option.

### Acceptance test

The Claude Free integration is complete only when all of the following pass:

1. A public HTTPS MCP URL is reachable by Anthropic.
2. The connector is added to a Free Claude account as its single custom connector.
3. Invalid or missing credentials receive HTTP 401.
4. Claude can call device_status.
5. Claude can start a harmless bounded job and poll it by job_id.
6. Claude can read job output with a cursor without receiving unbounded stdout.
7. Claude can read an allowed file and a forbidden path is rejected.
8. A destructive operation cannot bypass MCPshka policy.
9. No inbound port is opened on the home router; the path from the home device to the public edge is outbound-initiated.

## Sources

- Anthropic Help Center: Get started with custom connectors using remote MCP
  https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp
- Anthropic Help Center: Install Claude Desktop
  https://support.claude.com/en/articles/10065433-install-claude-desktop
