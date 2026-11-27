# MCPshka 0.1.154

Paired controller + broker release on top of 0.1.153. It wires the native
integration pipeline end to end (BUG-20261020-8): a whole-module test, a
Face ID-issued approval and a protected store that main follows.

## Integration pipeline

1. `integration_enqueue` admits a candidate branch, commit, target head and
   ROADMAP scope (unchanged).
2. `integration_test_prepare` (Face ID session) seals a **whole-module**
   permit `project-module-test-v1`: `go test -p 1 -count=1 ./...` over the
   committed `go.mod`, `go.sum`, `cmd`, `internal`, offline in the existing
   bubblewrap sandbox. Dependencies are copied only for `go.sum` entries
   present in root's verified module cache; nothing is fetched. Build cache and
   temp files use a disk-backed per-permit scratch; the unit gets two CPUs,
   1600M and one hour. Preparation is asynchronous (it may outlast the broker's
   30 s request budget) and idempotent per repository and commit.
3. The test runs, is observed and fenced through the existing route
   `exec_start command=mcpshka args=[executor-canary, launch|status|canary-fence, permit, launch]`.
   A whole-module launch is refused while another one builds or the host is
   saturated (load > 8 or < 700 MiB available).
4. `integration_approve` (Face ID session) issues the root-sealed protected
   approval: root checks the live branch and target heads, the fenced
   successful whole-module result for the exact commit and the protected
   store; the reviewer is the Face ID owner and the review digest is the
   `target..candidate` diff.
5. `integration_worker_launch` imports objects into the protected store
   `/root/repos/mcpshka-controlled/<store>.git` and fast-forwards its target
   (unchanged root worker).
6. `integration_publish_main` fast-forwards the working checkout
   (`/root/repos/mcpshka` main) to the candidate once root reports the claim
   RELEASED. It needs an idle project, a free writer lease and a clean checkout
   at the admitted target head; a repeat is a no-op.

## Fixes

- **The lease holder can work in its own project (BUG-20261020-19).**
  `exec_start`, `project_test` and `project_build` accept `lease_token`;
  admission checks the exact current interactive holder and keeps workflow
  fencing. `fs_write` gains the same token path `fs_patch` had. Tokens enter
  idempotency receipts only as digests.
- **Face ID grants end with the task (BUG-20261020-23).** An administrative
  session expires 24 h after Face ID unless closed earlier; expiry is derived
  on read, so the record stays the audit and a new Face ID starts the next
  generation. Status reports `grant_expires_at`.
- **Codex task submission into long sessions (BUG-20261020-17/18).** The
  bridge matched submissions against full turn items (5.9 MB for a long
  session, above the 2 MiB websocket frame), so every `codex_task_submit`
  before-probe failed. Summary items keep `userMessage.clientId`. Failures now
  report the exception class.

## Operations

- One-time: the protected store parent `/root/repos/mcpshka-controlled`
  (root, 0700) and store `f4bf4ece6766df20cb72d59f3ab2c4e8` were created with
  `mcpshka-broker executor-prepare-repository`.
- After deployment the session granted on 2026-10-17 is expired by design;
  the first integration needs one fresh Face ID.

## Known limits

- A dead holder of a sibling-worktree lease is still not reclaimable
  (BUG-20261020-20); administrative recovery covers main only.
- `project_test` has no package filter yet (BUG-20261020-24).
- GitHub `main` is not updated by the pipeline; merge the PR there.
