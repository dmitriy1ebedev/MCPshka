# MCPshka 0.1.87

Integrates chatgpt/codex-handoff-support-20261009 through a history-preserving merge.
Adds read-only codex_capacity_status: bounded session-state evidence, unknown quota/reset as null, and no automatic handoff or lease release.
Unknown daemon states fail closed. MCP regressions reject fabricated quota and preserve the artifact-upload surface from 0.1.86.

Acceptance: full Go tests, vet, diff review, clean committed build, guarded deploy, version/hash/rollback/tunnel verification and a read-only live Codex session probe.
Isolated executor and lease transfer remain pending their separate security gates.
