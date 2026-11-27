# Bounded test tiers

Run `python3 -I -B packaging/native/test-plan.py FAST` to inspect the plan.
Add `--run` through an unprivileged managed job to execute it. The builtin
MCPshka project profile exposes `fast`, `integration`, `release`, and `production`.

FAST selects changed Go packages and their transitive consumers, including test
imports. Unknown Go changes and packaging contracts conservatively select all Go
packages. Native helper changes run all native contracts. Each step reports why it
runs. INTEGRATION adds vet for the selected Go packages.

RELEASE requires a clean commit and runs all Go tests, vet, and all native
contracts **on a cloud runner**. Race is NOT repeated inside RELEASE: the separate
mandatory `race` cloud job owns race testing. Full release acceptance requires
five successful GitHub Actions jobs for the identical source commit/tree:
`release`, `race`, `fuzz`, `build`, `release-arm64`.
A single successful local or cloud job is not sufficient to release. Release
also requires security review, protected-root acceptance, reproducible candidate
validation, guarded deploy and verification. PRODUCTION invokes the installed
production verifier on H96.

On production-class ARM64 machines (under 5 GiB RAM), FAST/INTEGRATION
run only a bounded lightweight allowlist (`filesystem`, `policy`, `audit`,
`trust`) and **defer** all other Go packages, including `mcpserver`, to cloud CI. Their
machine-readable report carries `deferred_remote` and
`acceptance=local_smoke_only_cloud_required`; never represent a fast run
as full acceptance. RELEASE `--run` refuses execution on such hosts rather
than exhausting H96 RAM or timing out. Dry-run RELEASE still reports the plan.
The host limitation is architectural, not a test failure or permission shortcut.

The GitHub workflow runs all five suites on every PR (two concurrent runners
maximum) and permits individual suite dispatch for exact committed SHA.
Release artifacts are pinned to exact CI outcomes before guarded activation.
Time-boxed local tests should target small packages; Go's `-run` selector
does not avoid compilation of the whole package.

Go tests retain `-p 1` and disable test-result caching with `-count=1`.
Compilation uses Go's content/toolchain/flag keyed cache. The plan reports an
input fingerprint covering source, toolchain, and environment without exporting
environment values. Command reports include duration, CPU, peak RSS, and IO blocks
when GNU time is available. Timeout or interruption terminates the command group,
including descendants left behind by a completed leader.

The fuzz targets cover MCP objects, mutation retry keys, scope recovery evidence,
ROADMAP serialization, and queue recovery transitions. Normal tests execute their
regression seeds. Prolonged fuzzing, extended race, chaos, and load campaigns belong
on an isolated cloud runner; local tiers do not enable them. Cloud execution and
overall before/after performance validation remain pending.
