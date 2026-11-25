# Distributed CI and unified release

**Goal:** parallel remote CI on GitHub, GitVerse and Mos.Hub with only guarded binary download/deploy/smoke on H96. Treat each provider as an independent testing executor, never as a production controller.

| Provider | Proposed work | Current facts |
| --- | --- | --- |
| GitHub | RELEASE, race, fuzz, canonical signed release binary | Existing cloud.yml supports four checks. Successful historical runs do not prove current commit. Private repo attestations depend on account eligibility. |
| GitVerse | ARM64/AMD64 cross-build, vet, compatibility | Existing workflow; snapshot mirror rewrites commit history. Recent remote write was blocked. Cloud CI capped at 30m/job; quota must be checked. |
| Mos.Hub | SAST/SCA, dependencies, source/package checks | Public platform supports CI; account API, runner and quotas not yet verified. No access or credential exists. |

## Proposed MCP tools and hooks

The first native Go MCP tool is `cloud_fanin` (read-only, schema-validated, bounded in-memory observations). It never authenticates provider claims or authorizes deployment. The independent Python `fanin.py` is its CI-side analogue; the provider-facing API is not implemented yet.

Planned MCP tools: cloud_dispatch, cloud_status, cloud_reconcile, release_gate, release_artifact, release_promote. They should use existing durable jobs, idempotency, policy, lease and deploy. Provider adapters isolate auth tokens and enforce egress/response caps.

Durable event hooks: candidate.created -> dispatch; ci.changed -> reconcile; gates.verified -> artifact promotion; deploy.completed -> production verification. Hooks must not execute commands from remote webhook payloads. Current packaging/cloud/hooks.py emits only safe, read-only hints, not events wired into MCPshka.

Developer preview: packaging/cloud/fanin.py --plan PLAN.json --receipts-dir DIR. The plan has schema mcpshka.cloud-plan/v1, release_id, source_digest (sha256) and required mapping of provider to suites. Receipts have schema mcpshka.cloud-observation/v1, same IDs, provider, suite, run_id and status. The result is UNTRUSTED even if it says observed_pass; deploy_authorized is always false. Strict invalid/missing/repeated/mismatched records HOLD or FAIL; these records must be independently verified with authenticated provider API and artifact provenance.

The GitHub opt-in `release-arm64` suite cross-compiles an actual Linux ARM64 CGO-enabled executable with the AArch64 GCC toolchain and saves its SHA-256/build metadata. This artifact is explicitly diagnostic and UNSIGNED; it is not eligible for automated promotion until authenticated provenance, exact source matching, gate verification and the guarded download contract exist. No H96 compilation is needed for that check.

Never assume parentless GitVerse snapshot SHA equals local Git SHA. Canonical release identity is deterministic source content digest shared across mirrors. The canonical ARM64 binary must be CGO-enabled and linked as expected for SQLite; prior cross-build harness emits *diagnostic* binaries, not deployable release candidates.

## Codex Cloud task handoff

The native read-only `codex_cloud_observe` tool accepts at most 64 KiB of externally supplied `codex cloud list --json` output. It checks a caller-pinned cloud environment and source digest, accepts at most 20 tasks, hashes titles, rejects duplicates/foreign environments and treats unrecognized statuses as UNKNOWN. A pagination cursor makes `complete_page=false`.

These observations are UNTRUSTED. They do not authenticate the cloud account, verify its checkout/source, claim a local session, infer token quota, grant a writer lease, dispatch Codex, apply a diff or authorize deployment. A task seen in Codex Cloud is not equivalent to a local `codex_sessions_list` identity.

Authenticated Codex Cloud dispatch/apply requires a separate provider adapter, pinned source identity, explicit budget and owner/writer policy. The `codex` CLI was not found in MCPshka's executable PATH in this check; do not work around the policy by reading credential stores or starting competing workers. Copilot cloud agent access and paid entitlement are not verified; GitVerse writes are blocked; Mos.Hub account CI is unverified.

Acceptance: three independent verified providers; signatures/provenance; exactly pinned source/artifact digests; bounded retry and recovery; no production credentials outside H96; production deploy + reconnect + smoke + rollback readiness; measured reduction of H96 compute. No DONE or ROADMAP checkbox until those proofs exist.

## MosHub snapshot adapter checkpoint — 2026-10-16

- **Network discovery:** H96 reached the MosHub HTTPS origin. Without credentials, `/api/v4/version` yielded HTTP 401 and `/projects/my` redirected repeatedly to login. This is not proof of full GitLab API compatibility.
- **Existing remotes:** `github`, `origin`; no authenticated MosHub remote, project path, deploy key, API token or pinned known_hosts was established. **No remote write was attempted.**
- **New isolated development branch:** `feat/moshub-snapshots-20261016`; `packaging/moshub/snapshot.py` creates a future private-only immutable, source-SHA-pinned snapshot ref, never a forced `main` rewrite. Untrusted public visibility, missing credentials, symlinks, changed source and ref collision fail closed. `plan` does no network or mutation.
- **Offline regression:** `python3 -I packaging/moshub/test_snapshot.py` passed 5 tests on 2026-10-16; include repeat acceptance after review. See `packaging/moshub/README.md` for verified setup steps, security contracts and recovery.
- **External setup gate:** owner signs into <https://hub.mos.ru/projects/my>, selects/creates private repo and provisions a dedicated scoped SSH credential and API credential via a local secure approval flow. A real publish requires authenticated private visibility and pinned SSH host key, then read-back SHA and independent CI job evidence. Existing GitHub CI remains the authoritative build gate; do not claim MosHub runner tested until it really runs.
