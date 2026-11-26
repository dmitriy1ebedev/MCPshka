# 0.1.131 — SQLite receipt decoding

The SQLite CLI reports the effective `.limit length 262144` before its query
result. The privileged release gate previously treated that report as receipt
hex, holding stale-deployment retirement before any fence was written.

Decode the exact limit report and one receipt row separately. Preserve the
SQLite resource limit, service-owner execution, read-only safe mode, timeout,
payload bound and SHA-256 verification. Unexpected reports, additional rows or
changed hashes remain HOLD. Tests exercise the real SQLite CLI on an isolated
database and reject altered limits, duplicate rows and corrupt hashes.

Publishing the frontend does not replace the installed privileged gate.
Existing UNKNOWN retirement receipts remain authoritative and are never replayed
or rewritten by this fix. Reconcile the effect and approve the exact corrected
gate before a new deliberate retirement request.
