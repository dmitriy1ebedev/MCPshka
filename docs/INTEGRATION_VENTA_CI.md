# MCP-269: root-verified GitHub CI for the Venta project

This is a narrow, first-party **trusted project policy**, not a user-defined
GitHub repository switch. The root broker accepts provider=`ci` for
`/root/repos/venta-aw730-homeassistant` only after validating the local
candidate SHA and exactly one local branch. It cannot be redirected to an
arbitrary repository or source by a PR, README, local configuration file or
tool caller.

In the restricted network preparation unit, MCPshka fetches the GitHub API
for the compiled-in repository `dmitriy1ebedev/venta-aw730-homeassistant`.
It requires exactly one ready open PR into `main` from the local branch,
owned by that same repository. A GitHub compare must report its base as an
ancestor of the PR head (behind=0), not a synthetic conflicting merge.
The remote commit's complete Git tree must equal the pinned local commit's
Git tree even if the Git SHA differs because author or parent metadata was
rewritten when publishing.

All five exact named checks (Python, HACS, hassfest, and two separate
Home Assistant version smokes) must be *completed/success*, from the
authenticated remote head and attached to that exact PR and base. Missing
checks, a later failed rerun, a different source tree, foreign CI, a draft
or moved PR, or changed base all fail closed. No user-supplied required-check
set is accepted. The root-issued evidence pins both source commit and remote
head, and the fenced offline evidence worker rechecks the sealed manifest
before the human-authorized integration worker may run.

For this project the CI publisher does not issue a GitHub job itself:
`integration_ship` directly invokes the root **verification** step, whose
own GitHub queries enforce the complete check set. Other repositories
continue to be blocked from provider CI unless explicitly implemented and
tested with a separate compiled-in root trust policy. Standard MCPshka
CI, GitVerse, distributed shards and Go module evidence remain unchanged.

**Security and rollout:** This code is not an installation. Full Go CI,
independent review, protected source integration, Face ID approval and a
separate signed 0.17.x MCPshka release are required before live use.
The Venta 1.2.0 package likewise must pass its own project_release recipe,
snapshot/rollback and real HomeKit/LocalTuya checks after this dependency is
installed. No production Home Assistant or Apple Home pairing data is sent
to a CI provider by this feature.
