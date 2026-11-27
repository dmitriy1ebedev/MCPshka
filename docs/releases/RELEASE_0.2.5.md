# MCPshka 0.2.5 — work lives in MCPshka, any chat can drive it

Date: 2026-10-27.

## M1: MCPshka runs the development, the chat only sets tasks (Codex, Claude review)
- `roadmap_run` takes an arbitrary `goal` + `path` (no ROADMAP needed); the runner has Codex/Claude implement it, validates with sealed `project_test` and `phase_gate` itself, and keeps going when the chat is closed.
- `roadmap_run_status` without arguments is the global view: active and latest runs across all projects, goal, phase, agent, step, last job/gate, blocker and next_action.
- BUG-20261027-138: `agent_task` survives a quota limit: with a known reset the runner waits (releasing the heavy queue) and resumes automatically with `resume_of`/`resumed_by` (8 h, 3 continuations). Each attempt runs in its own bounded unit, so a waiting runner never makes an ended agent's lease look live.
- Registered Go trees get read-only module dependencies from the trusted cache in sealed tests (offline, as the module gate).

## Compact surface for chat clients
- `/mcp?tools=core`: ~22 stable tools (hand off work, statuses, phase_gate, knowledge, Face ID) with the same handlers, gates and receipts as the full server. Every other tool stays reachable with its live schema via `tool_resolve` → `tool_invoke`, so ChatGPT, DeepSeek or Qwen keep the full power without a 250-tool list going stale after each release.

## Fixed
- BUG-20261026-137: a FAILED preparation returns `reason`, a bounded `log_excerpt` of its own unit and `next_action` instead of a bare HOLD.
- `release_local` stuck in COLLECTING: the release guide routes through `release_apply` with the sealed build permit.
