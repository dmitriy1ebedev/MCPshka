# MCPshka 0.1.114 — distributed development baseline

This is the staged versioned release candidate after individually
reviewed and merged PR #15 (read-only mount-boundary inventory),
PR #16 (private-only MosHub immutable snapshot publisher),
PR #17 (GitVerse immutable snapshot publisher and GigaIDE Cloud guide),
and PR #18 (bounded GitVerse snapshot CI).

## Verified prior production

0.1.113 remains installed on H96 at the point of candidate creation;
0.1.112 is the live rollback. MCPshka service/tunnel/health/DNS/HTTPS and
main Git cleanliness were checked and passed. No OOB remote backup exists;
the only management path is the primary tunnel, so this release uses
existing guarded self-deploy with automatic rollback, never direct network
mutation or Docker restart.

## Independent provider evidence

- GitVerse SSH read succeeded after owner enrolled public key; its
  RSA server fingerprint matches published official documentation.
- Initial immutable GitVerse snapshot, source tree
  `4982f2c01be5aa9b53a0db9424ed43deb98f9c83` committed remotely;
  a second snapshot of combined PR17/PR18 source tree
  `fb57dcd5ce793b3fe8059c2cfb57be35fb4a016f` was published and
  remote readback verified. Remote main was not overwritten.
- GitVerse runner execution is **NOT YET VERIFIED**. Workflow now
  includes `mcpshka-snapshots/**` trigger, gated at 10 minutes and
  targeted read-only contracts. A Git write is not a CI pass.
- MosHub API authenticated without exporting secrets; private project
  `lda6994/mcpshka` is observed. SSH pin and live Git/CI remain HOLD.
- GigaIDE Cloud has not been launched. Owner must sign into Cloud.ru,
  accept offer and examine free/paid quota to avoid unapproved charges.
- Codex is notLoaded; Claude Code and GigaChat provider adapters and
  time-bounded Face ID administrative broker are still separate P12/P13/P26
  work, NOT part of this release.

## Release gate (must not precheck)

- [ ] Five authenticated cloud suites PASS against identical release
  head/tree and canonical source digest, including ARM64 CGO binary.
- [ ] Artifact ZIP digest, ELF SHA, current and rollback binary identities
  match signed operator-verified release plan.
- [ ] Guarded preflight permits staged self-deploy, no conflicting writer.
- [ ] Detached deploy succeeds; production 0.1.114, rollback 0.1.113,
  actual binary SHA, health, tunnel, DNS/HTTPS and emergency path PASS.
- [ ] Cleanup or durable next-step checkpoint; no guessed 100%.
