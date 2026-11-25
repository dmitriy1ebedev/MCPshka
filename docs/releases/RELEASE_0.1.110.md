# MCPshka 0.1.110

The passkey approval page preserves the original WebAuthn options across repeated login and registration attempts. A retry decodes a fresh copy instead of calling string decoding on an already converted byte array.

A mutation receipt lookup preserves missing-record identity. The MCP mutation probe returns `outcome: not_found` with `retry_allowed: false` when no durable receipt exists. Pending outcomes remain `UNKNOWN`; neither response authorizes replay or a new mutation key.

Scope-aware recovery preserves principal isolation: a missing receipt for a principal-bound yield, recovery or integration mutation returns a generic unavailable error, not a successful not_found response. The full cloud RELEASE caught this cross-principal regression and requires a clean rerun.

The release keeps the existing authorization, full RELEASE gate, guarded deployment and rollback requirements. Real owner WebAuthn approval followed by automatic Home Assistant execution, durable receipt and verification/rollback requires physical acceptance.

Integration candidate also includes the read-only distributed CI fan-in,
Codex Cloud/Copilot observations, SQLite cloud task journal, environment-pinned
Codex Cloud preview, bounded 64-KiB source hashing, fail-closed P8 rollup, and
R3 orphan-claim visibility. Existing newer state-store and R3 validation are
retained instead of reverting to the older PR bases.

Acceptance is pending on the exact integrated tree. Historical PR results do
not cover this source. Protected-core activation, physical owner acceptance,
authenticated external cloud dispatch and signed release provenance remain
separate gates. This document does not declare deployment or ROADMAP DONE.

Independent integration review corrected interrupted request-plan rebinding,
malformed cloud pages being reported as complete empty pages, stalled archive
stream deadlines, and additional SSH host-key trust. Regressions failed before
each fix. Local cloud Python (21), SSH preflight (6), full healthlayer/cloudtasks/
codexcoord and focused MCP tests passed. Full remote gates and production
acceptance remain pending; no historical CI result is substituted for them.

Signed root-only release admission now seals exact human-authorized plans with
short-lived Ed25519 approval, one-use launch fencing and detached guarded
deployment. The helper verifies current/staged binary SHA pins before mutation
or candidate execution. This is local root approval, not cloud attestation;
see RELEASE_PROMOTION.md. Fresh integrated CI and activation remain required.
