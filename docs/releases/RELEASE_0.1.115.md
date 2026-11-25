# MCPshka 0.1.115 — GitVerse CI observer and session handoff

This release is intentionally small. Production baseline was 0.1.114,
with rollback 0.1.113. The GitVerse SSH mirror and immutable snapshot
publishing are already working and verified on H96.

## What changed

- Adds `packaging/gitverse/ci_status.py`: a bounded, read-only,
  token-file-based GitVerse Actions API observer. It validates an exact
  snapshot branch and optional Git head SHA, refuses redirects and proxies,
  caps response size/time/count and never outputs a bearer token or raw
  provider response. No CI observation authorizes deploy. Malformed,
  partial, incomplete, missing/unauthorized provider responses remain HOLD.
- Adds five offline tests, including wrong source, unknown state, partial
  pagination and malformed provider records. No cloud token or network
  access is necessary to run unit tests.
- Bumps MCPshka version 0.1.114 -> 0.1.115.

## GitVerse completion truth

Authenticated SSH read and write **already PASS**. On 2026-10-16, source
`bf0407d83d8509ead051420f08dba3e325d3c4db` and Git tree
`1b8fac9739caefca625ff89c654f5b52e2785f25` were published as
`refs/heads/mcpshka-snapshots/bf0407d83d8509ead051420f08dba3e325d3c4db`;
GitVerse remote snapshot commit
`7c2d7de1dfe6fa8c53f71d87c1ed56a56e797e5b`, readback verified.
The GitVerse `main` branch remains unchanged.

**GitVerse CI verification is not DONE**: a separate GitVerse Public API
read-only repository token is missing. Official API at
`https://api.gitverse.ru/repos/dmitriy_1ebedev/mcpshka/actions/runs`
requires `Authorization: Bearer` and repository read scope; probing without
token returned HTTP 401. The SSH deploy key cannot be used as API auth.
A successful snapshot push does not prove GitVerse CI ran.

The token must be provisioned locally to
`/var/lib/mcpshka/gitverse/api_token`, owner `mcpshka`, mode `0600`,
parent `0700`, via reviewed secret input (never the chat, a Git worktree
or a job argv). The observer accepts:
`python3 -I packaging/gitverse/ci_status.py --branch mcpshka-snapshots/bf0407d83d8509ead051420f08dba3e325d3c4db --expected-head 7c2d7de1dfe6fa8c53f71d87c1ed56a56e797e5b`.
The status still stays UNTRUSTED until independent provider receipts and
source identity are reconciled; observed_pass is *not* deploy authorization.

## Mandatory promotion

All five GitHub CI suites must PASS on exact reviewed release head,
GitHub/local Git trees must match, pinned Linux ARM64 binary and receipt
must be verified, and a guarded self_deploy preflight + detached installer
with rollback must PASS. Reconnect and independently call
`project_verify(expected_version="0.1.115")` and
`control_path_status`; cleanup only afterwards. Never assume production
updated just because GitHub merged.

The local ROADMAP is the execution authority; it includes Codex/Claude
Code/GigaChat provider adapters, protected-core Face ID admin sessions,
P4 OOB control backup, MosHub SSH/pipeline and GigaIDE billing blockers.
Do not mark those DONE here or create duplicate tools.
