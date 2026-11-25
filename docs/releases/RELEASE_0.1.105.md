# Release 0.1.105 — SQLite control-plane recovery

Fix state status and already-active authority validation above 10,000 records without deleting records or widening bounded List/export/migration limits. Native helper status validates every payload and checksum with bounded memory, including records after the former limit. The emergency compressed bootstrap is preserved locally and removed from the test entrypoint.

This release does not activate unattended Codex execution, alter Face ID/broker permissions or change network services. Existing R3 work remains preserved for reconciliation.

Release gates and deployment evidence are recorded in the local consolidation checkpoint; this note alone is not deployment proof.
