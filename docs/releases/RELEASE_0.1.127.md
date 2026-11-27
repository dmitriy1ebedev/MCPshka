# 0.1.127 — durable deployment reconciliation

A lost `systemd-run` reply now retains the original accepted receipt and returns
its deployment ID with UNKNOWN. It cannot overwrite a helper's independently
persisted success or rollback result. Never retry an uncertain deployment with a
new key.

For new MCP attempts, the existing durable PENDING receipt reserves a gate-compatible
deployment ID before any launch (receipt timestamp plus a domain-separated hash of
its retry key and fingerprint). `mutation_probe` recovers that exact handle after a
controller restart, even when the helper has already finished. The versioned inner
operation prevents guessing IDs for legacy random-ID attempts. Observation leaves
PENDING unchanged; a recovered handle never grants replay. Initial deployment
receipts are exclusive and cannot overwrite an existing deployment identity.

`project_deploy_reconcile`, `project_context.deployment_reconciliation` and
`mcpshka deployment-status [deployment_id]` expose bounded durable observations.
Every observation includes the original deployment ID as its correlation ID.
Accepted/running records older than the helper's five-minute deadline are explicit
`STALE_DEPLOYMENT_UNKNOWN`; unknown timestamps and outcomes remain HOLD. Reads do
not clear historical uncertainty or grant deployment/lease authority. Missing,
corrupt, mismatched or over-budget authority fails closed.

The release contract remains: prepare digest-pinned cloud artifact; authenticate
all five CI gates and approve the exact plan; deploy once through the existing
signed root gate; reconcile status after reconnect; verify actual binary hashes,
health/tunnel and rollback image. The detached helper survives controller/tunnel
loss and owns automatic rollback. A reverse release uses the same independently
approved transition; age, a newer installed version or missing transient unit
never authorizes replay or fabricated failure.

Baseline audit (2026-10-16): production 0.1.126, rollback 0.1.123; installed root
gate and guarded helper byte-match repository packaging. Legacy 0.1.124 receipt
`1792159092900-1e327387d562` remains accepted with no unit evidence; preserve HOLD.
Codex CLI 0.163.0 and local daemon are reachable; this task's existing session is
loaded/active. Task submission requires an already loaded owner session; an absent
session is not resumed or replaced by another writer.

DNS observation only: dnscrypt-proxy active; dnsmasq fails to bind
192.168.77.11:53 (address in use). No DNS/network configuration or services changed.
These observations are evidence, not acceptance of P4/P12 or a new OOB path.
