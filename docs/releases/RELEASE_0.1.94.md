# MCPshka 0.1.94

Integration coordination now shares one configured runtime across the MCP API,
job admission and maintenance. Root-issued integration approvals bind the
candidate, target, repository identity, sealed test result and review evidence.
Repository reservations retain immutable completion records across restart and
reject stale generations or corrupted completion state.

The integration recovery supervisor binds an operation to its registered worker,
requires closed admission and a fresh process-incarnation check, and inspects
the target and integration receipt without repeating Git updates. Recovery uses
fixed permit and approval IDs through the root broker. MCP mutation authorization
and receipts are owner-bound; compatibility calls probe the root integration
claim before, after and following a lost response.

Root snapshot reads now use the service account's actual primary group while
retaining fixed UID 994 and clearing supplementary groups. Missing or invalid
account credentials fail closed. Sandboxed jobs prefer the system Go toolchain
over an older installation under `/usr/local/go`.

The native writer barrier and integration worker launcher remain unfinished.
Live recovery therefore holds unresolved operations; these changes do not enable
automatic takeover of legacy leases or the complete autonomous release loop.
Existing executor fencing and guarded deployment policy continue to apply.
