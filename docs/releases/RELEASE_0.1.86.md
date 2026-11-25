# MCPshka 0.1.86

Adds a bounded durable MCP artifact inbox for explicitly supplied file bytes:
allocation, resumable chunks, status, checksum seal and incomplete-upload cancellation.
Native MCP tools and cached-schema compatibility are included.

Ownership checks cover tool calls, idempotency probes, cached receipts and compatibility job views.
Symlinks, hardlinks, unsafe file ownership/modes, conflicting offsets and checksums are rejected.
Durable chunk intents distinguish interrupted writes from unexplained content.
Inbox reservations, directory fsync and disk reserves protect H96 resources.

This release does not activate lease transfer or claim the isolated executor complete.
Native ChatGPT fileParams download integration and the missing Companion extension remain pending.
No protected-core source or DIAG production changes are included.

Acceptance requires targeted fault/security tests, full Go tests, vet, independent review,
a build from clean committed Git, guarded deployment, production health/tunnel/rollback checks
and a real MCP ingress canary with matching SHA-256. Evidence is recorded in local checkpoints.
See [MCP artifact ingress](MCP_ARTIFACT_INGRESS.md).
