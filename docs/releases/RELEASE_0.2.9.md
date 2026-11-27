# MCPshka 0.2.9 - sealed tests retain their execution budget

Date: 2026-10-27.

## Fix
- BUG-150: the root executor persists the one-use launch reservation time. The workbench controller starts the execution deadline from that durable reservation, or the later heavy-queue admission, instead of charging snapshot preparation against the test budget.
- Existing receipts without a launch timestamp fall back to root permit creation after preparation, then to the original job timestamp if neither root timestamp exists.
- The 1800-second recipe policy limit, cancellation, one-use launch admission and verified resource fencing remain enforced. A lost launch reply never creates a second reservation.

- `forge_ci` accepts the current writer lease token and passes it only to the existing holder admission guard. Tokenless or foreign holders remain blocked; receipts bind the token digest. This does not resolve historical UNKNOWN publication receipts or authorize replay.

- `mutation_probe` reads `forge_ci` handler receipts under the authenticated owner's key. A definite recorded refusal can authorize a corrected request with a new key; pending or uncertain receipts remain UNKNOWN. Foreign and unscoped handler receipts cannot authorize retry, and historical receipts are preserved.

- The runner's explicit-goal prompt permits production work only when the owner's task explicitly authorizes it, with the existing test, approval and fencing gates. Phase prompts retain the production restriction. This does not change an active executor's instructions or grant new permissions.

- After a lost sealed test admission reply, the runner probes the original mutation receipt and observes its recorded job. It verifies the job identity and sealed runtime before waiting; missing receipts or unavailable status remain UNKNOWN without a second admission.

- Heartbeat recovery first observes the live lease and replays only the original heartbeat key within a bounded deadline. Cancellation, an unverified heartbeat, or an UNKNOWN sealed test preserves the runner lease and WIP for reconciliation instead of releasing writer authority.
- SQLite coprocess startup honors caller cancellation and retains the underlying initialization error. Canceled operations cannot initialize a connection or execute SQL. The historical heartbeat's discarded SQLite diagnostics do not establish its exact low-level cause.

## Checks
- A regression test first reproduced a freshly sealed permit being fenced before launch.
- Tests cover preparation delay, durable launch time, an earlier or later queue timestamp, deadline expiry, cancellation and legacy receipts.
- The search reply optimization already in main is retained; superseded branch implementations are not copied over it. No new benchmark improvement or live acceptance is claimed.

## Release gates
- Exact-commit CI, protected integration, sealed acceptance, coordinated installation and production verification are required. This document is not evidence that those gates passed.

## Rollback
- The previous installed controller/broker pair is 0.2.8; coordinated release must retain and verify that pair for rollback.
