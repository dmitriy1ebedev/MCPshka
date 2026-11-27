# MCPshka 0.1.62 production acceptance — 2026-10-08

Deployment `1791451164768-d04461754125` SUCCEEDED. Source branch
`work/tier1-consolidated-20261008`, baseline `012b43f` (0.1.61),
commits `1d26ee2` (conflict-resolved P7 freshness/heartbeat) and
`b32fe60` (Docker resources warning-safe strict JSON and regression tests).

Installed ARM64 SHA256:
`423d32d86a10d36832ec24d19543cfd8bf4395bed3866ae4e34cdb0e10a7f943`.
Installed version 0.1.62. Last-known-good rollback 0.1.61.

Acceptance: targeted dockerops/projectlease/mcpserver tests PASS;
full go test -p 1 ./... PASS; candidate version/ELF ARM64/SHA PASS;
guarded detached deployment, reconnect, MCPSHKA/tunnel active,
healthz/tunnel ready, SQLite, DNS, outbound HTTPS, Docker metadata and
Home Assistant healthy. Live docker_resources(rootledger-app-1) succeeds
despite Docker config warning. Root main Git remains dirty only with known
unrelated README.md and error.log; these are untouched. Therefore aggregate
project_verify.ok=false ONLY due to git_clean=false; all runtime gates passed.

Pending: 42 RootLedger Uvicorn-owned zombie children and 1 Codex zombie
still exist; actual fix requires scoped RootLedger source/allowlisted restart,
not killing zombies blindly. Codex resumed but read-only sandbox forbids
access to /root/repos; no Codex code edits. Main/DIAG/P7 old leases must not
be stolen solely based on age. Rootbroker P4 protected code and incomplete
iCloud upload helpers excluded from this production binary.

DIAG 0.1.1 release capsule at
`/var/lib/chatops/projects/diag/releases/.staging-0.1.1-0edfb80`
has SEALED status and verified SHA256 for source archive, ARM64/AMD64 diag
and diagd, and tar.gz capsule; DIAG production remains 0.2.a.3.1.
Native/Docker/Astra smoke, verified iCloud remote receipt and guarded
DIAG production deploy remain incomplete. Do not call DIAG production DONE.
