# MCPshka 0.1.107 — Codex visibility and recovery

This release integrates existing Codex bridge work without starting a second executor.

- Bounded session reads preserve the newest activity after a long initial prompt.
- Command observations include status and safe command identity; arbitrary arguments remain withheld.
- Recovery correlates deterministic request IDs with queue and turn evidence, including linked continuations and bounded pagination.
- Missing queue evidence remains UNKNOWN. Approval/input waits remain owner-controlled. Token usage is never treated as account quota.
- Mutation before/after evidence retains submission identity after a lost reply.

The installed Python bridge must match the release source. A binary-only deployment cannot activate this API safely. Existing authorization gates remain required.

Testing: existing bridge regressions and additional negative boundary checks; targeted Go tests; full fixed-SHA RELEASE gate. Production acceptance and helper rollback receipts are recorded separately after guarded deployment.

Full durable dispatch, automatic owner-approved privileged execution, and cloud runner integration remain separate required work.

## Production acceptance

Deployed and independently verified on 2026-10-14 from commit `b5da6c5c5966fbedec703634573665af2af833b8`. Binary SHA-256: `8058a33e373de8416e0b6cc7a28393161ae90f9453ce83cadce00661b8626a2a`. Immediate rollback: 0.1.106, SHA-256 `27b280ad28a8134b4da7006e6b08dbc30bb83a218d18a52817eb32cfd5abe3ef`.

Full RELEASE gate passed in 1043.874s (all Go tests, vet, targeted race, 52 native tests). Builds took 95.022s and 7.647s and matched byte-for-byte. Guarded deploy took28s. Native production verification passed version, rollback, service/tunnel health, DNS and HTTPS. Live session reads expose command status and safe identity; queue absence remains UNKNOWN.

Known client limitation: an older connector schema can discard optional request/turn/cursor fields. Those inputs require a refreshed client schema; session reads and queue-ID probes were verified through the existing connector. Complete orchestration and cloud execution remain separate work.
