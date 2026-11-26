# MCPshka 0.1.116 — GitVerse native CI observation fix

## Scope
One bounded repair of the existing read-only GitVerse observer; no second CI provider or release gate.

The GitVerse Actions list API documents `ref` (full refs/heads/ path), `commit_sha`, `event`, `status`, `total_count` and `workflow_runs`. The previous observer incorrectly required GitHub-shaped `head_branch` and `head_sha`, and incorrectly modeled completed runs as having an obligatory `conclusion`.

The observer filters push events in the API query and validates the exact immutable snapshot branch and Git SHA locally, together with total-count pagination and duplicate run IDs. A live GitVerse query on 2026-10-16 established that a slash-containing branch query returns HTTP 400 even for an existing snapshot, while the event-filtered query works. The live API uses terminal status `success`/`failure`; the documented `completed` without conclusion remains HOLD. All positive observations stay UNTRUSTED and never authorize deployment.

## Acceptance
- Targeted offline GitVerse observer tests and complete packaging/gitverse contracts.
- `git diff --check`, review, atomic commit of 0.1.116.
- Mandatory fresh GitHub release/race/fuzz/build/release-arm64 checks on exact release source, ARM64 CGO artifact and source digest, guarded deploy, reconnect, production 0.1.116 verification, retained rollback 0.1.115.
- Live authenticated GitVerse status smoke PASS: snapshot `mcpshka-snapshots/bf0407d83d8509ead051420f08dba3e325d3c4db`, remote commit `7c2d7de1dfe6fa8c53f71d87c1ed56a56e797e5b`, run ID `1832227`, terminal status `success`, complete response, observer `observed_pass` / `UNTRUSTED` / `deploy_authorized=false`. The API token already exists; do not provision a duplicate or leak its contents. GitVerse observation never substitutes for authenticated GitHub CI release gates.
- P4 independent OOB path remains NOT DONE; no production fault injection allowed.

Official contract: https://gitverse.ru/docs/developers/public-api/actions/api-get-repos-actions-runs
