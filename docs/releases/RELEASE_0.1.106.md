# 0.1.106 — test workflow optimization

Deployed and verified on 2026-10-14. Production subsequently advanced to 0.1.107; 0.1.106 is the immediate rollback image.

This release follows deployed recovery 0.1.105. It introduces FAST,
INTEGRATION, RELEASE, and PRODUCTION workflows, with dependency-aware Go test
selection, explicit reasons, bounded command groups, and resource reports.
Compilation is cached; test verdicts are rerun. Go parallelism remains `-p 1`.
The full release test/vet/targeted-race/native gate is retained.

Five fuzz targets exercise MCP JSON, mutation retry keys, scope recovery,
ROADMAP serialization, and queue recovery. Regression seeds run normally;
prolonged fuzzing remains off H96. Seed findings add narrowly scoped safeguards
against ROADMAP serialization loss and queue revision overflow.
The managed handoff regression uses a worker rendezvous instead of a timing
assumption, so fencing is checked while the first worker is still active.

Codex orchestration, Codex bridge changes, Face ID/root broker changes, rescue
installer updates, and cloud-agent integration are saved for subsequent releases.
Release checks, before/after workload measurements, reproducible build,
guarded deploy, production verification, and rollback verification are required
before this candidate is accepted.
