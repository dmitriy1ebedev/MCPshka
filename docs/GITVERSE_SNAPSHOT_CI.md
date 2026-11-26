# GitVerse snapshot quick-check admission

This independent GitVerse workflow runs only for immutable
`mcpshka-snapshots/<40-hex-source-sha>` branches. It does not replace
GitHub release/race/fuzz/build/ARM64 gates. It has a 10-minute limit,
one writer per Go compilation, read-only code checks, no secrets, no
Docker socket or root privileges, no access to H96 production, and no deploy.

`/.gitverse/workflows/snapshot-contracts.yml` is inert on GitHub. On
GitVerse, `on.push.branches: ['mcpshka-snapshots/**']` is supported per
https://gitverse.ru/docs/cicd/docs/workflow ; GitVerse CI/CD must
first be enabled in repository settings. Initial `main`-only workflow
does not trigger snapshot pushes, so this dedicated workflow lives
on future snapshot trees and will be tested with one new immutable ref.

A GitVerse runner's output must be authenticated against a specific
snapshot ref SHA and its source tree before admission to the distributed
CI fan-in. Any absent runner, missing API auth, unknown status or
mismatched source remains HOLD. No claim of real GitVerse CI execution
until live run ID, exit and result have been observed by a trusted API.

GigaIDE Cloud is a separate owner-controlled workspace: Cloud.ru SSO
and offer acceptance and billing review required. No workspace launch
is authorized by this workflow. This lane supports P26/P30 distributed CI.
