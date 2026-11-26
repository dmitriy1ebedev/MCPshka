# Release 0.1.55 — production acceptance 2026-10-08

Version numbering is now stage.tier.release: 0 = alpha, 1 = Tier 1,
55 = sequential release number. See VERSIONING.md. Legacy release identities
and rollback names are unchanged.

Source commit: 0036132. Built from an isolated git archive of that commit using
`go build -p 1 -trimpath -buildvcs=false`. Unknown README changes were preserved
and excluded. Candidate and installed SHA-256:
`8e092ad1423a6c47770cbeae73f2a69bc14f58d4d325e4636890b37b146e3473`.

`go test -p 1 ./...`, `go vet -p 1 ./...`, five statestore tests,
two SQLite idempotency tests and the offline harness's four tests pass.
The new-format deployment test accepts a new identity over a legacy version.
Candidate and installed binaries pass an isolated unprivileged MCP
initialization, version and command-classification smoke.

Guarded deploy 1791429568919-b9f632366858 succeeded at 03:19:37 UTC.
Installed version/hash, healthz and readyz match. The real MCP connector
reconnected successfully. Both services remain active; DNS and outbound
HTTPS connectivity pass; all 11 containers retain their previous status.
NoNewPrivileges remains enabled. Rollback is executable 0.1.a.54 with SHA-256
`e504ccb15994be2499432d63f049c221b8013951e1732c2dd22b611ba0ec1ca2`.

This release contains a tested but inactive P3 SQLite backend and explicit
idempotency adapter. Runtime store migration, backup automation and tool
contract registration remain pending. P3 is IN PROGRESS, not DONE.
