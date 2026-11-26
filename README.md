<h1 align="center">MCPshka</h1>

<p align="center">
  <img src="https://img.shields.io/badge/version-0.19.4-brightgreen" alt="version 0.19.4">
  <a href="https://github.com/dmitriy1ebedev/MCPshka/actions/workflows/open-core.yml"><img src="https://github.com/dmitriy1ebedev/MCPshka/actions/workflows/open-core.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go" alt="Go 1.26">
  <img src="https://img.shields.io/badge/platform-linux%20arm64%20%7C%20amd64-lightgrey" alt="Linux arm64 | amd64">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache-2.0"></a>
</p>

<p align="center"><b>English</b> · <a href="README.ru.md">Русский</a></p>

**MCPshka** is a self-hosted [MCP](https://modelcontextprotocol.io) server that lets AI chats (ChatGPT, Claude, GigaChat and others) operate a Linux host safely: run jobs, edit code, test, release and drive a smart home — with hard output limits, an audit trail and owner approval (Face ID) for anything dangerous.

It is one native Go binary built for a small always-on ARM64 box, and it develops itself: the AI that talks to MCPshka writes, tests, merges and installs the next MCPshka release through MCPshka's own tools.

## Features

- **Bounded by design** — every command becomes a job; output is read by cursor with a server-side cap, so nothing floods the model context.
- **Remote desktop for an AI** — processes, files, search and precise edits in the spirit of Desktop Commander, over stdio or Streamable HTTP.
- **Owner in the loop** — destructive, privileged and release actions need a one-time WebAuthn (Face ID) approval; tokens are stored only as hashes.
- **Single-writer projects** — per-worktree leases, idempotency keys on every mutation, durable jobs that survive restarts.
- **Sealed CI and releases** — sandboxed test runs on the host, an integration queue with fast-forward merges, coordinated upgrades with rollback.
- **Distributed CI** — test shards fan out to GitHub, GitVerse, SourceCraft and MosHub; if every site is down, the host runs the shards itself.
- **Code Mode** — one `code_run` JavaScript script calls many tools in parallel instead of dozens of chat round-trips.
- **Smart home and network** — Home Assistant, Docker and systemd under allowlists; network changes go through a staged provider with automatic rollback.
- **Many brains** — GigaChat and free LLM providers through one gateway with secret redaction; Codex and Claude Code as delegated agents.
- **Memory** — a local knowledge base the AI can recall and update across chats.

## Contents

- [Quick start](#quick-start)
- [Installation](#installation)
- [Connecting an AI client](#connecting-an-ai-client)
- [Configuration](#configuration)
- [Architecture](#architecture)
- [Security](#security)
- [Project status](#project-status)
- [Acknowledgements](#acknowledgements)
- [License](#license)

## Quick start

Requires Go 1.26+ and Linux.

```sh
git clone https://github.com/dmitriy1ebedev/MCPshka.git
cd MCPshka
go build -o mcpshka ./cmd/mcpshka
./mcpshka version
./mcpshka status        # compact host snapshot
./mcpshka mcp-stdio     # MCP over stdio for a local client
```

## Installation

For a permanent install as a hardened systemd service (state under `/var/lib/mcpshka`, HTTP on `127.0.0.1:8765`, a generated token registry in `/etc/mcpshka`):

```sh
go build -trimpath -o bin/mcpshka ./cmd/mcpshka
sudo packaging/native/install.sh
```

The root broker for sealed tests and releases is installed separately with `packaging/native/install-broker.sh`.

`packaging/native/uninstall.sh` removes it. Docker is supported as an optional isolation mode, not a requirement.

## Connecting an AI client

- **Local client (stdio):** point the client at `mcpshka mcp-stdio`.
- **Remote client (HTTP):** `MCPSHKA_TOKEN=… mcpshka mcp-http` listens on loopback only; expose it through an authenticated outbound tunnel (no inbound router port is needed). See [docs/CLIENT_COMPATIBILITY.md](docs/CLIENT_COMPATIBILITY.md).

The default catalog shows about 40 tools; `capabilities_bootstrap` and `tool_resolve` let the AI discover the full set on demand.

## Configuration

| Variable | Purpose |
|---|---|
| `MCPSHKA_HTTP_ADDR` | HTTP listen address (loopback) |
| `MCPSHKA_AUTH_FILE` | Token registry: hashed tokens, roles, scopes, expiry, rotation without restart |
| `MCPSHKA_TOKEN` | Single bearer token (legacy, when no registry is set) |
| `MCPSHKA_DEFAULT_ROLE_PROFILE` | `operator` (default) or `reader` (read-only) |
| `MCPSHKA_PROJECT_PROFILES` | Per-project role overrides, JSON |
| `MCPSHKA_SYSTEMD_SERVICES`, `MCPSHKA_DOCKER_READ_CONTAINERS` | Allowlists for service and container access |

Projects describe their own test/build workflows in `.mcpshka/profile.json` (schema `mcpshka.project-profile/v1`, direct argv, no shell strings).

## Architecture

```
AI chat ──MCP──► mcpshka (service user)  ──► jobs, files, git, projects, CI
                    │
                    └─unix socket──► mcpshka-broker (root) ──► sealed executor, releases, systemd
```

- `cmd/mcpshka` — MCP server and CLI; `cmd/mcpshka-broker` — root broker; `cmd/ci-shard` — distributed CI worker.
- `internal/tools/*` — one package per tool family; MCP is an adapter, not the core.
- Design notes: [docs/INVARIANTS.md](docs/INVARIANTS.md), [docs/EXECUTOR_RUNTIME.md](docs/EXECUTOR_RUNTIME.md), [docs/DISTRIBUTED_CI.md](docs/DISTRIBUTED_CI.md).

## Security

- The HTTP listener binds to loopback; remote access goes through an authenticated tunnel.
- Command classes (read-only, mutating, destructive, privileged) route risk; destructive and privileged actions need a one-time owner approval.
- Command classes are not a sandbox: an allowed interpreter can still run arbitrary code, so code under test runs in the sealed executor without network.
- Secrets never enter tool output or argv; the audit log redacts arguments.
- The public tree is exported from the private repository with a secret and identifier audit; personal hosts and keys are replaced by documentation addresses.

## Project status

Alpha, in daily production on one home host since autumn 2026. APIs and tool names still change between releases. Changes: [CHANGELOG.md](CHANGELOG.md), release notes: [docs/releases](docs/releases).

Mirrors: [GitHub](https://github.com/dmitriy1ebedev/MCPshka) · [GitVerse](https://gitverse.ru/dmitriy_1ebedev/MCPshka) · [SourceCraft](https://sourcecraft.dev/lda6994/mcpshka) · [MosHub](https://hub.mos.ru/lda6994/mcpshka).

## Acknowledgements

MCPshka is built on other people's good ideas:

- [Desktop Commander MCP](https://github.com/wonderwhy-er/DesktopCommanderMCP) — the "remote desktop for an AI chat" model: process sessions, windowed reads, precise edits.
- [Model Context Protocol](https://modelcontextprotocol.io) and the [official Go SDK](https://github.com/modelcontextprotocol/go-sdk).
- [go2rtc](https://github.com/AlexxIT/go2rtc) — proof that one small Go binary can run a whole home subsystem.
- [Home Assistant](https://github.com/home-assistant/core) — the smart home MCPshka drives.
- [Zuul project gating](https://zuul-ci.org/docs/zuul/latest/gating.html) — the integration queue.
- [Temporal](https://github.com/temporalio/sdk-go) and [go-workflows](https://github.com/cschleiden/go-workflows) — durable, resumable work.
- [Litestream](https://github.com/benbjohnson/litestream) and [age](https://github.com/FiloSottile/age) — SQLite backups and their encryption.
- [tusd](https://github.com/tus/tusd), [gobreaker](https://github.com/sony/gobreaker), [OpenTelemetry Go](https://github.com/open-telemetry/opentelemetry-go).
- [OpenAI Codex CLI](https://github.com/openai/codex) and [freellmapi](https://github.com/tashfeenahmed/freellmapi).
- [Uncle Bob's](https://github.com/unclebob) [crapper](https://github.com/unclebob/crapper), [mutator](https://github.com/unclebob/mutator) and [dryer](https://github.com/unclebob/dryer) — code health checks.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE).
