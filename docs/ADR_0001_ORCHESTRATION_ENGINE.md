# ADR 0001 — durable orchestration engine: build vs buy (MCP-069)

Status: accepted for 0.3.0 · Date: 2026-11-02 · Host: H96 (aarch64, 4 cores, 3.7 GiB RAM, production with HA/Zigbee/Docker)

## Context

MCPshka needs durable jobs, leases, fenced claims, idempotent mutations,
UNKNOWN reconciliation and crash recovery. Candidates: keep the in-house
engine (SQLite `records` + statestore + heavy queue + systemd scopes) or
adopt an external workflow engine.

## Measurements on H96 (2026-11-02, live production)

| component | RSS | threads | state on disk |
|---|---|---|---|
| mcpshka (MCP server + job manager + all state machines) | 77 MiB | 13 | state.db 32 MiB |
| mcpshka-broker (root broker) | 15 MiB | 12 | executor state.db |
| total in-house engine | ~92 MiB | 25 | single SQLite file per trust domain |

Free memory on the host at measurement time: ~750 MiB available of 3.7 GiB.

## Options

| criterion | in-house (current) | Temporal (server + DB) | River / Asynq-style queue (Postgres/Redis) |
|---|---|---|---|
| extra resident memory on ARM64 | 0 | server + Postgres/Cassandra: hundreds of MiB to >1 GiB | Postgres or Redis: 50–200 MiB plus worker |
| new always-on services on production | none | 2–4 | 1–2 |
| root/service trust split (broker vs service DB) | native | needs two namespaces/clusters | not modelled |
| fencing by generation / lease token / boot id | implemented and tested (docs/INVARIANTS.md) | activity heartbeats, no host process fencing | job locks only |
| UNKNOWN effect, never replay a mutation | explicit receipts (`I-JOB-2`, `I-INT-1`) | retries by default; must be disabled per activity | retries by default |
| systemd scope/cgroup ownership of processes | yes | no (workers are opaque) | no |
| offline operation (no internet) | yes | yes | yes |
| migration cost | 0 | rewrite of every state machine | rewrite of queue and claims |

## Decision

Keep the in-house engine. An external engine would add 2–4 always-on
services and hundreds of MiB on a 3.7 GiB production host shared with Home
Assistant, without covering the root/service trust split, systemd process
ownership or the "UNKNOWN is never replayed" rule; those would still have to
be written around it.

## Consequences

- Invariants stay explicit and CI-checked (docs/INVARIANTS.md).
- Revisit when any of: a second always-on host joins the executor
  (MCP-099 node agent), event-ledger replay (MCP-062) needs cross-host
  ordering, or RSS of the engine exceeds 256 MiB.
