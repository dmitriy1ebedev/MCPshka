# MCPshka 0.2.7 — search replies fit the budget in one pass

Date: 2026-10-27.

## Performance (measured)
- `searchindex.Search` fits the reply to the byte budget by encoding each selected hit once and counting the JSON array size, instead of re-marshalling the shrinking reply after every dropped hit. Same results, ordering, single-oversize error and serialization errors as before.
- Medians of 5 runs on H96 (Codex 5b510e96, benchmark jobs mvqiamqp-d/mvqic4tf-f, mvqidv7m-h/mvqiet3y-i): 100 records/100 hits needing trim 57.5 ms → 2.8 ms, 2.95 MB → 0.16 MB per op; 1000/100 trim 65.6 ms → 37.1 ms; 1000/100 fitting entirely unchanged (19.3 ms). No claim for other paths.
