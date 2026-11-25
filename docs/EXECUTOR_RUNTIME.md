# Isolated executor runtime

Tests run only through `project_test` (`packages`/`run` select a subset);
`exec_start` refuses `go test`, `pytest` and `npm test` with
`TEST_ROUTE_PROJECT_TEST` (MCP-150). `project_test` uses a private `0700`
temporary directory inside the durable job directory by default. This avoids false disk-reserve failures when the host `/tmp`
is a small tmpfs. An explicit `TMPDIR` remains unchanged; other commands retain
their existing behavior. Temporary files follow the existing job reaper and
descendant checks. This changes temporary storage only, not sandbox admission,
filesystem permissions, execution authority or the minimum free-space reserve.

The privileged controller stores permits, launch identities and results in root-owned SQLite.
A permit schedules one fixed transient unit. Source, generation and execution identities
are pinned before scheduling; an unresolved launch remains HOLD.

Two bootstrap profiles exist. Only a local root administrator can prepare them:

* `executor-prepare-canary BOOTSTRAP_ID`: fixed fork-writer isolation regression.
* `executor-prepare-test BOOTSTRAP_ID REPOSITORY COMMIT internal/PACKAGE`:
  committed read-only Go package tests. This copies regular Git blobs into a private
  snapshot, including go.mod/go.sum. It excludes shared Git metadata and rejects
  symbolic links, hard links and unsupported Git modes. Snapshot preparation is
  bounded to 10,000 files and 64 MiB. Its digest is verified before execution.
  Materialization anchors creation to the fresh private base using os.Root;
  directory modes are set through opened descriptors. An escaping destination
  symlink is rejected before files or permissions outside that base can change.
  This assumes the trusted root-only constructor and its private mount topology;
  it does not accept caller-controlled base paths or claim bind-mount isolation.

The test profile mounts its source read-only. Writable /tmp and /run are separate
tmpfs mounts capped at 256 MiB and 16 MiB, with a 512 MiB cgroup memory ceiling.
No network, host credentials, broker socket or authority storage reaches guest code.
Offline leaf packages work immediately; missing dependencies fail explicitly.
There is no package installation, writable source export or production deployment.

The existing MCP `exec_start` compatibility route accepts fixed identities:

```
command: mcpshka
args: [executor-canary, launch|status|canary-fence, PERMIT_ID, LAUNCH_ID]
```

Use a distinct durable idempotency key for each intended operation.
An accepted launch is LAUNCH_INTENT, not a successful test.
After a test process exits, the root worker saves a bounded result with exit code,
untrusted output and process identity. Status remains EXITED_UNFENCED.
The registered trusted leader stays alive for fencing, within the unit deadline.
A separately verified fence drains the exact cgroup and changes the visible result
to SUCCEEDED or FAILED. A result receipt never substitutes for fence evidence.
Results survive controller restart. Unknown operations require reconciliation.

These test permits do not transfer a project lease or authorize a project writer.
Administrative lease transfer and legacy writer fencing remain HOLD.

The local root operator can prepare a private bare Git store with
`mcpshka-broker executor-prepare-repository REPOSITORY_ID`. The ID is exactly
32 lowercase hexadecimal characters. The approved installer must first prepare
the fixed root-only parent `/root/repos/mcpshka-controlled` and executor authority.
The constructor journals creation and returns the sealed storage identity;
replay verifies that identity and never adopts an existing unknown directory.
It creates no worktree and grants no worker, integration or cleanup permission.
There is no MCP route for this operator action.

`mcpshka-broker executor-prepare-branch STORE_ID RESOURCE_ID TEST_PERMIT_ID BRANCH`
creates a requested development branch in an existing protected store. Its
commit must already have a verified object graph there. The owner is derived
from the sealed successful test permit and freshly verified stopped incarnation.
The root repository lock serializes creation; main/master and invalid Git refs
are rejected. The name remains reserved to the exact birth request after crash.
Creation records the new ref inode before publication, then confirms `READY`;
replay checks the inode and commit without recreating or adopting another ref.
An interrupted unrecorded inode remains HOLD. No legacy branch or worktree is
adopted. This constructor grants no integration or cleanup permission.

Integration intent may carry `owned_branch_id`. It remains untrusted at MCP
admission. Root approval requires the exact sealed repository, ref, commit,
test permit and launch, live inode provenance and fresh stopped fencing. The
resource is reserved to one approval ID; retries of that approval are allowed,
while another task receives HOLD. Ownership participates in the approval digest.
Cold inspection keeps immutable birth/reservation pins readable independently
of the ref, so a future journaled retirement cannot erase historical evidence.

After a successful integration reaches `RELEASED`, root maintenance retires its
owned development branch only after fresh writer fencing and ancestry checks.
It first publishes an inode-pinned recovery ref with a verified object graph,
then journals `DELETE_INTENT` before removing the exact original ref inode.
Replay completes the journal without repeating deletion; replaced refs, missing
recovery objects, active writers and unknown ownership stay HOLD. Failed
integrations preserve their branch. Birth and approval evidence remain readable.
This retires protected owned branches only; linked worktree cleanup is not
supported by the protected bare-store layout.

## Real namespace acceptance

The `MCPshka real Workbench isolation` cloud workflow requires a non-root runner
and builds non-setuid bubblewrap 0.12.0 from a pinned upstream commit and SHA-256.
It does not disable host protections or install anything on H96. The explicit
`MCPSHKA_SANDBOX_E2E=1` test uses real `workerArgs` namespaces and synthetic host
files/listeners. Python, Bash command substitution, Make and npm scripts must
fail to access outside files, host process roots, broker/Docker paths and host
sockets; guest capabilities, environment and snapshot permissions are checked.
Named broker/Docker paths are absence checks in this synthetic runner; live
canaries/listeners supply the positive controls. The procfs view must match the
guest PID namespace. Deliberately exposing the host canary, host network or
host procfs must fail the intended
assertion, proving the harness detects these faults.

The project-test case executes the unchanged `go test -p 1 ./internal/probe`
command. A minimal offline Go package calls Python, Bash, Make and npm and
requires all four namespace, read-only source and host-access probes to pass.
The Go build starts with empty private tmpfs caches under the existing 256 MiB
limit. CI places its pinned Go toolchain under `/usr/local/go` on the disposable
runner so the existing `/usr` mount exposes it; no sandbox mount or command is
substituted. Its version and bounded copy size/free space are checked.

Root constructor/permit admission, systemd cgroup limits, descendant fencing,
concurrent project leases and native H96 end-to-end execution need their own
acceptance. This workflow supplements the five release gates and does not mark
full P2 complete. Default unit runs explicitly skip this cloud-only test;
opted-in runs fail if the sandbox is unavailable. The workflow requires all
eight workload/fault subcases to pass without skips, including the actual Go
package run with all four nested workloads. A cached/no-tests result is rejected.
