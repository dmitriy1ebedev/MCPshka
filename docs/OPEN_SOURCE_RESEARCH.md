# Open-source research for MCPshka

This document records architectural ideas worth reusing. It does not mean source code has been copied.

## DesktopCommanderMCP — MIT

Useful ideas:
- long-running terminal sessions;
- separate start/read/interact lifecycle;
- output pagination and tail reads;
- recursive listing limits;
- search sessions with pagination;
- targeted old/new text editing;
- local tool-call history;
- Docker isolation option.

MCPshka changes:
- jobs are first-class and independent of an MCP session;
- stdout/stderr are disk-backed from the start;
- byte quotas are enforced server-side;
- shell interpretation is not the default execution path;
- core logic is independent from MCP.

## MCP Filesystem reference server — Apache-2.0 / legacy MIT contributions

Useful ideas:
- explicit allowed roots;
- dynamic access boundaries;
- clear read/write tool separation;
- tool annotations for read-only, idempotent and destructive behavior;
- Docker mount-based isolation.

MCPshka changes:
- roots belong to a local policy engine, not to a client-controlled setting;
- writes will use atomic replacement and optimistic concurrency.

## mcp-shell-server — MIT

Useful ideas:
- argv execution rather than shell strings;
- minimal child environment;
- bounded timeout and output;
- structured audit logs;
- contained redirection;
- explicit statement that an allowlist is not a sandbox.

This is the main security model inspiration for MCPshka.

## ssh-mcp — MIT

Useful ideas:
- role/profile policy;
- command classification;
- approval modes;
- bounded interactive sessions;
- idle/background TTL;
- process reaper;
- host-key trust model;
- file transfer that bypasses model context;
- rate limits and auditability.

MCPshka will apply these ideas locally first and to multi-host mode later.

## Official MCP Go SDK — MIT-style license

Use directly as a dependency.
Reasons:
- official protocol implementation;
- typed tools and schemas;
- current MCP protocol compatibility;
- stdio and Streamable HTTP transports;
- no need to maintain our own JSON-RPC/MCP framing.

## creack/pty — MIT

Candidate for the future interactive terminal layer.
Use only when a real PTY is needed. Normal jobs should continue to use non-PTY pipes/files because they are simpler and easier to bound.

## Charm Wish — MIT

Architecture inspiration for an optional SSH control surface:
- SSH key identity;
- middleware;
- no accidental default shell;
- PTY-aware sessions.

Not needed in the first remote transport.

## chisel — MIT and frp — Apache-2.0

Architecture inspiration for outbound/reverse connectivity through NAT:
- client-initiated connection;
- keepalive;
- multiplexing;
- authentication;
- reconnect;
- health/status.

MCPshka should not invent cryptography or a generic proxy. A future relay should carry only the MCPshka protocol.

## tmux / terminal multiplexers

Idea only:
- named sessions;
- detach/re-attach;
- session survives client disconnect;
- explicit lifecycle.

MCPshka jobs should have these semantics without requiring tmux.

## OpenHands / coding-agent runtimes

Idea only:
- separate agent protocol from runtime execution;
- sandbox/runtime provider abstraction;
- event-driven task lifecycle;
- Docker as an isolation backend.

MCPshka remains much smaller: it is an execution/runtime service, not an AI agent.

## Principles selected for MCPshka

1. One small native agent.
2. Jobs outlive a single request.
3. Never return unbounded stdout.
4. Files and artifacts stay local until explicitly requested.
5. Direct argv is the normal execution primitive.
6. MCP is one front-end, not the core.
7. OS-enforced isolation is preferred over clever command filtering.
8. Destructive operations are separate from ordinary execution.
9. Every mutation should be auditable and ideally idempotent.
10. Remote access should be outbound-only or private-network-first.
