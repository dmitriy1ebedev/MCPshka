# Cloud development — 0.1.108

H96 owns production policy, approvals and deployment. Cloud environments receive
reviewed code and checks only; never production credentials, Tailscale identity,
root broker authorization or deployment rights.

## GitVerse CI

The existing CI and historical runs are independent of GigaIDE Cloud workspaces.
The manual `.github/workflows/cloud.yml` runs a pinned, clean commit with one
of four explicit suites: release (existing full gate), extended race, bounded
fuzzing of the existing targets, or the existing ARM64/AMD64 lab cross-build.
The shared workflow resides in `.github/workflows`, a directory also supported
by GitVerse; provider execution remains unverified. GitHub token permissions are
read-only. Cross-build results are diagnostics, not production SQLite binaries. It requires an unprivileged runner; a root-only
runner is blocked rather than bypassing the test planner gate. Logs are limited
to 32 MiB; jobs to 30 minutes; report artifacts expire after three days. Checkout
does not persist credentials. Compiler caching does not cache passing verdicts.

Pass the exact remote commit as expected_sha. A moving branch, dirty checkout,
unknown suite or unavailable runner fails the gate. Cloud reports do not imply
H96 deployment or verification. Source changes are reviewed and integrated by
the H96 owner after results are collected.

## GigaIDE Cloud

The owner created a workspace on 2026-10-14 with 2 vCPU and 8 GiB RAM. The
supplied URL currently redirects an unauthenticated H96 client to GitVerse SSO.
A screenshot proves workspace creation, not agent authentication or current code.
The repository shown contains an older snapshot; verify its exact SHA before
running checks. Do not overwrite workspace changes or request passwords/tokens
in chat. Use an authenticated owner-approved access mechanism.

## Verified and blocked

The GitHub full release gate passed on 2026-10-14 in run 38607628845, job
116002472803: all four checks (Go tests, vet, race, native Python tests) exited
zero in 249.272 seconds of check execution. Tested checkout:
`bc0f4093f8adc0ae76261ea7dadb8efbc00c8c1c`; PR head:
`57895a90c43d260b140d650b7b6e7d1abde55523`. Its source tree
`b262c8b41eea29eddef533d09c4a463e30027d72` matches local commit
`b4ecffed5d6dbfebcb1119612e7be8a8c3f7280a`. Evidence:
https://github.com/dmitriy1ebedev/mcpshka/actions/runs/38607628845
and draft PR https://github.com/dmitriy1ebedev/mcpshka/pull/1.
Artifact 11917236297 SHA-256:
`bf2678fbf4d61ff125e33020449b84047584278086b18ee3b72686ab45bb6434`.
Artifacts expire; retain these identities and reconcile the current provider run
before trusting results for a later release SHA.

The historical jobs cleanup regression passed 100 repetitions; cancellation now
waits for actual persistence completion. Clean-runner failures were reproduced
and fixed by explicitly private terminal test directories and a pinned websockets
dependency installed outside the checkout. Runtime broker checks stay strict.
The native suite passed 61 tests locally. `cloud-results.py` validates the pinned
SHA, plan/check correspondence and exit codes; incomplete evidence is UNKNOWN.
Its report is UNTRUSTED and never authorizes production deployment.

Extended race, fuzz and cross-build execution are now verified; see the
heavy validation receipt below. GitVerse reads
work, but the attempted source write returned transport AccessBlocked; no new
GitVerse run is proven. GigaIDE authentication, current-source transfer and result
collection remain unverified. Codespaces and cloud Codex handoff remain unverified;
local Codex session IDs are not proof of cloud-provider task visibility. The
old cloud helper UUID is absent from local Codex SQLite and returns `thread not
loaded` through the local app-server. Use a published cloud environment and
provider task identity; do not resume or manufacture a local replacement writer.

## Cost

Use only free resources. GitVerse's CI limits documentation says 500 private
repository minutes, while its current Free pricing page advertises 2,000 minutes.
These published values conflict: use the account's Limits page as authority;
remaining quota is unknown until observed. GigaIDE uses separate Cloud.ru compute,
memory and storage allowances; a workspace screenshot does not prove billing
settings or unlimited free usage. Do not enable paid capacity.
See https://gitverse.ru/docs/cicd/docs/limits and
https://gitverse.ru/home/pricing/ and the workspace billing settings.

Copilot Free includes limited coding assistance; Copilot cloud agent requires a
paid Copilot plan and is excluded from this free-only deployment. GigaCode's
personal IDE product is advertised as free, while GitVerse Free advertises its
integrated AI assistant for public repositories and Pro for private repositories.
Do not make MCPshka public to obtain AI access. Private cloud-agent entitlement
and remaining AI quota must be proven independently before dispatch.
See https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/start-copilot-sessions
and https://gitverse.ru/features/gigacode/pricing/.

## Production receipt — 2026-10-14

0.1.108 was deployed through guarded deployment `1792004978258-e75f20f78a7a`
in 29 seconds. Native production verification passed health, tunnel, DNS, HTTPS
and rollback readiness. Installed SHA-256:
`d3362d32083d28ddbbece2704b3f4d2da67dca3a8d581ac277e0d6d59d21f8c1`.
Rollback 0.1.107 SHA-256:
`8058a33e373de8416e0b6cc7a28393161ae90f9453ce83cadce00661b8626a2a`.
Source commit `f6d24eb1c3bd929fac595ffc658de055e6b010b8`; tree
`3f3730ae040d3b4cb8038922eb99f2cd89ae1056` matched the GitHub source tree.
The final full RELEASE gate passed in run `38620360406`, job `116045980068`,
checkout `b5fafc0eee68be4e1d1c02df4386e0cd7c369992`, with four zero exit
codes in 101.051 seconds. Artifact `11921656997` was downloaded and verified:
SHA-256 `a1dec2e1909c235c465a2f7466fe8f305a5b208c65ce5109e7d7d8932f030cfb`.
The warm compiler cache speeds compilation; test verdicts are never cached.
GitHub PR #1 is merged into main. Production uses the pinned release source;
main separately retains unfinished R3 and Face ID code for future releases.

The cloud skill is installed in the local Codex skill directory and validated.
Existing lifecycle hooks passed private hint/persistence checks; runtime hook
trust and actual cloud installation remain separate, unproven prerequisites.

## Opt-in heavy validation

Ordinary PRs run RELEASE only. A PR whose head branch is `ci/cloud-heavy`
runs RELEASE, extended race, fuzz and diagnostic cross-build as separate jobs,
with at most two hosted runners active. Manual dispatch still selects one suite.
Result artifacts include the suite name to prevent parallel upload collisions.
Read each job and artifact identity independently; one passing suite never proves
another. This opt-in path does not move production authority to the cloud.

## Heavy validation receipt — 2026-10-14

GitHub run `38626278978` passed RELEASE, full-project race, fuzz and both
cross-build architectures. All four downloaded result ZIP digests and checkout
`f7601122d4034e6e36ee04fa008bbb8af7d81372` were verified. RELEASE ran four
checks with zero exit codes in 153.588 seconds. Five fuzz targets passed for
30 seconds each, covering 1,983,902 generated inputs; no failing input was found.
Diagnostic ARM64/AMD64 builds passed; these CGO-disabled lab binaries were
removed by the existing build tool and are not production SQLite candidates.

PR #2 merged as `f4a0e2172e4d47a247551d53706f383d0be9401d`.
CI head `668ad472d78f5977745420dc9db7120e1c6c5855`; tree
`eb9e6b5ed022ddbd67e6144a459cdfd0d15ed305`. Result artifacts:
release `11923598732`, race `11923029954`, fuzz `11923489560`, build
`11924471195`. The durable checkpoint retains their verified SHA-256 digests.
Earlier fuzz attempts correctly failed on a compiler file-size cap and a cold
instrumentation timeout; both harness faults were reproduced and fixed. Log
limits now apply only to the log writer, compile/run timeout is 300 seconds per
target, and exploration remains 30 seconds. Failure corpus has a separate
three-day artifact so report ZIP paths stay stable.

This receipt covers the pinned CI snapshot, not pending Face ID 109 changes.
