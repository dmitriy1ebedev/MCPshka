# MosHub CI snapshots

MosHub is the third CI provider, alongside GitHub and GitVerse. The private
project is `lda6994/mcpshka-ci` (91385), accessed only through verified HTTPS.
SSH is not used: its host identity has not been established.

`python3 -B packaging/moshub/snapshot.py plan` reports the clean committed
HEAD and tree without reading credentials or accessing the network.
`publish` publishes a deterministic parentless commit with that exact tree to
`mcpshka-snapshots/<source SHA>`. Any clean source branch is accepted.
Existing refs must match exactly; remote `main` is never modified.
The source SHA and tree are recorded in the snapshot commit message.

Run publication as the owner of `/var/lib/mcpshka/moshub/api_token` (mode 0600).
The API uses a PRIVATE-TOKEN header without redirects. Git uses an ephemeral
askpass script in a 0700 directory; the script reads the token from its file.
Tokens are never placed in URLs, arguments, logs or Git configuration.
An authenticated API ref readback verifies publication. After an interrupted
push, retrying the same source reconciles the immutable ref without replacing it.

Offline checks:
`python3 -B -m unittest discover -s packaging/moshub -p 'test_*.py'`.
CI evidence does not authorize deployment.

## Pipeline status and live validation

`python3 -B packaging/moshub/ci_status.py <full source SHA>` reads the source
commit from this local repository and verifies the immutable snapshot identity,
pipeline ref/SHA and required job statuses. Output states are `passed`, `failed`,
`running`, or `missing`; only `passed` exits zero. API errors, unknown statuses,
truncated pagination and incomplete success evidence fail closed.

CI uses Go 1.26.0, the `appsechub` shared-runner tag, SQLite and an unprivileged
user. The arm64 matrix gets a 720-second timeout; other matrix callers retain
the 180-second default. Artifacts are the binary SHA-256 and a source/tree-bound
`mcpshka.cloud-results/v1` summary, retained for seven days.

Live observation, 2026-10-21:

- Source: `894c7285100c4734975c9f5d34be98e589a56d6c`.
- Tree: `54163388d0b46498e69706affa0b300bc52cee6d`.
- Snapshot: `f8521048aa7acbd5256c3c48b48bb3a32796b519`; API ref readback matched.
- Pipeline **89536**, job **346047**: **failed** during dependency setup.
  The shared runner could not connect to `deb.debian.org:443`, leaving SQLite
  unavailable. No test/build artifacts were produced by this final run.
- Earlier pipeline 89531 was canceled after revealing the required runner tag.
  Pipeline 89532 exercised tests and artifact upload, exposing missing SQLite,
  root restrictions and the cold-build timeout. Its failed summary was read
  back and matched its source/tree. Pipeline 89535 failed on Debian HTTP access.
- Fifteen offline adapter tests passed. A successful full cloud run remains
  unverified; retry the existing final pipeline once runner egress is restored.

No remote main branch is updated, and no production Go tests/builds run locally.
