# Signed release admission through MCP

An authenticated operator can complete a release from chat using the existing `project_deploy` tool. The wire schema and root broker command remain unchanged. The operator's call is the explicitly delegated release authority; caller-written CI observations or hook events alone never authorize execution. Reader identities cannot invoke the mutation.

## Workflow

1. Work in the managed project with one writer. Finish the requested ROADMAP/task slice, review and commit it.
2. Run the full independent remote release/race/fuzz/build/ARM64 CGO gates. Authenticate provider results, confirm the exact tested source tree/digest and artifact SHA-256, and retain their receipts.
3. Stage the verified binary in `/root/repos/mcpshka/bin/mcpshka-VERSION.SHA12.candidate` using existing bounded MCP file/job capabilities. Prepare the two JSON companion files below. Evidence preparation grants no additional authority.
4. Call `project_deploy` with that candidate, expected version, repository path and a fresh retry key. The existing authenticated operator call authorizes only the bounded MCPshka release transition. Missing/mismatched evidence fails closed.
5. Reconnect after the detached helper restarts MCPshka. Read `project_deploy_status`, verify actual installed/running identity, health, tunnel, DNS/HTTPS and rollback, then discover the updated MCP tools. Do not replay an UNKNOWN mutation.
6. Only after acceptance remove proven covered idle source branches/worktrees and temporary downloads.

No shell or new root bootstrap is needed for subsequent chat releases after the one-time reviewed wrapper installation.

## Companion contracts

`CANDIDATE.release-plan.json` is a strict JSON object:
- `schema`: `mcpshka.release-plan/v1`
- `version`, `current_version`: different bare `0.1.N` versions
- `candidate_sha256`, `current_sha256`: exact binary SHA-256
- `source_tree`: 40-character Git tree; `source_digest`: canonical release source SHA-256
- `ci_run`: positive authenticated GitHub run ID
- `ci_receipt_sha256`: SHA-256 of the exact receipt companion bytes
- `helper_sha256`: SHA-256 of the installed fixed root-owned guarded helper

`CANDIDATE.ci-receipt.json` has schema `mcpshka.operator-verified-ci/v1`, provider `github`, repository `dmitriy1ebedev/mcpshka`, `run_id`, `source_sha`, `head_sha`, `tree`, `source_digest`, conclusion `success`, exact `suites` mapping of release/race/fuzz/build/release-arm64 to `success`, plus `artifact_id`, `artifact_archive_sha256` and `binary_sha256`.

The gate rejects duplicate/extra JSON fields, missing gates, mismatched source/artifact identities and unsafe files. Receipt semantics and byte identity are checked locally; provider authentication is the operator workflow's separate responsibility. These fields are never a substitute for checking actual provider results.

## Root boundary

The one-time human-authorized bootstrap installs the reviewed isolated-Python `release-gate.py` at the existing `/usr/local/libexec/mcpshka-self-deploy` path, preserving its previous image, and the reviewed shell helper at fixed `/usr/local/libexec/mcpshka-self-deploy-guarded`. Both are immutable to the service user. The wrapper's existing six broker arguments are validated against fixed paths, versions, deployment ID and plan before authority writes. It reuses the original detached unit, deployment ID and status.

Only that fixed root-owned guarded helper can be sealed or executed. A repository script or caller-chosen helper hash cannot select privileged executable code. Updating either installed root helper remains a separate protected bootstrap operation; ordinary chat release admission promotes only MCPshka binaries.

The wrapper seals an Ed25519-signed approval and public verification key, candidate evidence copy, fixed helper and CI receipt in a private root-owned directory. The ephemeral signing secret is deleted. This signature authenticates the local delegated-root approval record; it is not a WebAuthn identity or independent cloud build signature.

The plan directory is exclusive and stable by full plan SHA. A 10-minute approval is verified and durably consumed before execution. Retrying the same plan after success, failure or UNKNOWN cannot mint another approval. Lost replies require status/unit reconciliation, never a new key alone.

The sealed fixed helper receives both binary SHA pins, checks current production under the deployment lock, and hashes the root-staged candidate before any candidate execution or rollback replacement. The sealed candidate copy is retained evidence; a mutable-inbox replacement can cause HOLD but cannot execute different bytes. The underlying shell helper retains legacy six-argument compatibility; signed MCP admission requires the SHA-named candidate and both companions.

The helper preserves production as rollback, restarts only MCPshka and its tunnel as needed, verifies local health/tunnel/DNS/HTTPS and rolls back on failed acceptance. Original deployment status records admission failures. Root wrapper work has CPU/address-space bounds; the existing detached unit has a hard timeout.

For offline plan validation only:
```text
python3 -I -S packaging/native/release-gate.py check-plan --plan PLAN.json
```

No new broker privileges, persistent signing key, authentication override or network listener is introduced.

## Compact chat workflow

Use release_prepare for bounded unprivileged artifact preparation and release_status for one-call polling. Both reuse native jobs; project_deploy remains the only promotion API. See [release skill](../skills/mcpshka-release/SKILL.md) for parameters, UNKNOWN recovery and partial-file reconciliation. Root admission independently verifies the exact plan and sealed fixed helper before consuming one-use authority.

From 0.1.145, preparation runs the installed `mcpshka release-artifact` native
worker. It downloads only the fixed artifact host or reads the private inbox,
validates the bounded ZIP and pinned source/ELF/SHA-256 identities, then stages
exclusive private files. It never evaluates repository scripts, executes the
candidate, installs binaries or grants deployment authority. Source tickets stay
in the private job environment. Historical Python job receipts remain readable;
an old UNKNOWN request is never cleared or replayed during migration.

For a controller that still requires the now-forbidden Python preparer, the
same reviewed Go worker is also available as `cmd/mcpshka-release-prep`. Build
that small bootstrap from committed, CI-verified source and run it as an
ordinary bounded unprivileged MCP job. This leaves interpreter policy and the
separate signed `project_deploy` admission intact. Retain its original job ID
and inspect output/status after interruption; never silently rerun it. Remove
only the owned bootstrap executable after the new installed worker is verified.


## Paired controller + root broker release (0.1.134 candidate)

This is a **separate explicitly human-root-authorized bootstrap**. It does not
weaken Face ID, expand the daemon's UID or allow an agent to overwrite the
protected broker from the unprivileged service. The legacy v1 `project_deploy` path updates only the controller.
**After the reviewed manual root bootstrap installs the v2 gate and guarded
helper**, a signed v2 `project_deploy` seals and upgrades both the controller
and broker in the same guarded deployment, verifies the running broker binary
and preserves both rollback images. A v1 plan never acquires broker authority.

The cloud `release-arm64` suite now packages two independent CGO-enabled
AArch64 executables from the same pinned source tree. The unprivileged native
`mcpshka release-artifact` worker validates both ELF identities,
buildinfo, SHA-256 and source metadata, stages:
`bin/mcpshka-VERSION.SHA12.candidate`,
`bin/mcpshka-broker-VERSION.SHA12.candidate`, and a
`bin/mcpshka-coordinated-release-VERSION.json` plan containing current and
candidate hashes and the exact reviewed installer digest.

**Physical root maintenance:** before an initial paired installation, an
independent operator must review the script/source hash, authenticate the exact
CI provenance, verify no other deployment/privileged task or root operation is
active, preserve access to the H96 host outside the MCP tunnel and ensure
rollback storage. Run the staged script with `--version VERSION --check`;
only then the **same reviewed script** with `--version VERSION --apply`
in a physical/authorized root terminal. Never paste arbitrary URLs into a
root shell. The command is not provided as a ready-to-run production command
until the candidate and verified CI receipt are staged and checked.

The operator must launch the initial bootstrap as a detached bounded
systemd unit from an independently authorized root terminal; running the
update foreground in a fragile SSH connection is not production acceptance.
The source script must first be hash-verified against reviewed CI source,
copied into a root-only immutable location and hash-verified again. The
first transition also pins and installs the new root-owned release-gate and
guarded helper. Thereafter only verified signed v2 release plans promote both
binaries through the existing `project_deploy` admission.

The paired helper takes the existing deployment lock, fails on replay/UNKNOWN,
uses atomic file replacement, keeps *both* previous executables and a
root-owned append-only phase journal, restarts only the broker/controller/tunnel
units, probes controller/tunnel and running broker hash, and reverts both on
a failed bounded health gate. Interrupted/UNKNOWN transitions must be
inspected from the root journal; no blind second `--apply`.

**Still open:** an authenticated Face ID-scoped broker-maintenance RPC with
root-owned dispatch/intent, independent fence/rollback acceptance and trusted
secondary management access. Until those are accepted, paired upgrades
require the explicit one-time root-terminal path above. Do not claim P4/P7 or
the administrative legacy-lease feature production-DONE merely because this
candidate and its tests pass.

## Owner console release apply

If the chat approval route becomes unavailable, first reconcile the original
operation. Keep its retry identity and any unresolved permit. The owner
console path works once the target is already protected main and both the
paired build and the exact GitHub CI test are independently sealed and fenced.
The CI evidence must include the successful ARM64 release check.

Before applying, verify the `phase_gate` and sealed test for that exact
commit, run and verify a backup of `state.db`, and confirm an independent H96
management connection. From the authorized local root console, run the
installed tool with the exact version, protected store, build permit and
test permit:

```text
/usr/local/libexec/mcpshka-broker release-owner-apply check VERSION STORE_ID BUILD_PERMIT TEST_PERMIT
/usr/local/libexec/mcpshka-broker release-owner-apply apply VERSION STORE_ID BUILD_PERMIT TEST_PERMIT
```

`check` verifies the root-owned evidence and returns the exact source and
image hashes. It leaves service and install state untouched. Run `apply`
once after confirming those hashes. It dispatches the same bounded root stage
unit and coordinated upgrade as `release_apply`. The coordinator preserves
both current binaries for rollback and writes the immutable upgrade journal.
The worker writes the release report with `origin=owner_console`. Only the
terminal successful journal plus the matching running version lets
`release_apply_status` return `INSTALLED`. The exact root-sealed CI evidence
is then available to `phase_gate`.

If a permit is still revoking, the protected main differs, a matching sealed
receipt is missing, or root apply already has history for this version, stop
and reconcile the original operation. Preserve the existing receipt. A
locally copied binary pair does not supply this evidence. Inspect status
after any lost reply. After installation verify the running version and both
hashes, broker/controller/tunnel health and restart counts, effective
`max_page_count`, TTL, and an ordinary MCP state write. Use the coordinated
rollback for a failed post-install check.

## Historical fence bridge (0.1.136, exact scope)

The human-authorized paired 0.1.134 transition completed successfully in
root-owned journal `b738d46dd7322e50e3768769fcd360a9`. One earlier
deployment, `1792159092900-1e327387d562`, has an immutable root fence
created while the previous gate/helper were installed. Replacing those binaries
must not erase or replay the historical UNKNOWN operation.

The root broker offers a read-only `historical-fence` verification for **only
that exact deployment**. It compares the original root-owned fence/receipt and
WebAuthn owner to hard-pinned release identities, the root-owned previous
gate/helper binary images, the currently installed gate/helper binaries, and the
root-owned terminal `SUCCEEDED` journal event with a later timestamp. Any
missing, aliased, modified, wrong-owner or mismatched evidence remains HOLD.
Successful proof means `FENCED_HISTORICAL_OUTCOME_UNKNOWN`, *not* success of
the original deployment. No root file is written, no approval widened, no
writer lease transferred, and no pending operation replayed.

The unprivileged controller accepts the bounded proof only over its existing
privileged broker socket and only with matching deployment, receipt, fence and
release-operation identities. Other deployments retain the strict current-gate
check and fail closed. The bridge is a temporary, exact-release compatibility
gate; future gate changes require a root-owned, version-independent signed
transition ledger with full replay and rollback acceptance. P4 independent OOB
access and general Face ID broker-maintenance admission remain separately open.

## Permanent historical retirement (0.1.146)

The old 0.1.124 receipt remains UNKNOWN and its original root fence is never
rewritten. The existing owner-authenticated `admin_session_retire_deployment`
operation can now seal that exact legacy operation into a private, exclusive,
fsynced root record, after rechecking the active administrative owner, current
controller SHA, deployment lock and complete historical root chain. No read-only
probe performs this migration. The existing bounded retirement worker and its
sandbox remain unchanged.

Once sealed, the broker returns `mcpshka.historical-fence-proof/v2` bound to the
original receipt, original fence and permanent retirement digest. It reads no
archived or installed installer images. The controller observes `retired=true`,
no remaining reconciliation action and zero unfinished deployments for this
retirement; it preserves the original outcome and audit identity. Closing the
administrative session does not revoke a completed nonreplayable retirement.

Missing, changed, aliased, writable, duplicate-field or partial retirement records
never authorize replay or overwrite. Reconcile the original request and root
record after a lost response. The original v1 bridge remains for the one-time
pre-migration compatibility path; changing installers before a verified permanent
record remains HOLD. Root evidence and approval policy are not removed.
# Retirement request rejection (0.1.147)

The fixed retirement V1 protocol requires a nonblank reason of at most 200 bytes,
without CR/LF/NUL or a leading dash. Validate it before allocating a mutation
receipt. These original broker checks run before worker launch.

An existing PENDING retirement with an exactly matching authenticated owner,
operation and payload fingerprint can be classified as rejected before execution
only when its reason violates those immutable V1 rules. SQLite compare-and-swap
records FAILED without invoking the broker. Conflicting, completed, valid UNKNOWN
or legacy file-backed records remain held. This is not a general error-to-FAILED
conversion and does not change deployment outcomes or fences.

`mutation_probe` reports `rejected_before_execution` only for this specific trusted
rejection marker; `retry_allowed` remains false. Inspect the rejection and resource
state before submitting a different corrected request. Never replay an UNKNOWN
deployment or use a new key to bypass a pending valid request.
