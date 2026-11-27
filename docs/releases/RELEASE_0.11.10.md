# MCPshka 0.11.10 — Git provider control plane (MCP-184)

Status: CANDIDATE. This document does **not** establish CI PASS, integration,
production deployment, or acceptance of MCP-184.

## Scope
- New typed GitProvider adapter for GitHub, GitVerse and MosHub: identity,
  capability inspection, repository list/get/create/update, branches, workflow
  runs, and bounded collaborator admission where the API is implemented.
- Repository creation is private when requested, excludes automatic README,
  reads the result back, and reconciles ambiguous POST outcomes with a GET.
  Never blindly retries a mutation with an UNKNOWN outcome.
- New MCP entrypoints: git_provider_query, git_provider_change and
  git_provider_push. Mutations require active owner-bound Face ID and
  idempotency. Push requires a canonical project lease and runs in a bounded
  job without generic shell execution.
- Git full-history push requires clean Git HEAD, exact registered HTTPS
  remote URL, non-forced fast-forward, scanned reachable patch history,
  service-controlled token file and post-operation remote SHA verification.
- Existing CI-specific forge_ci and GitVerse remote remain unchanged.

## Credential boundary
Credential files: /var/lib/mcpshka/{github,gitverse,moshub}/api_token.
Tokens are not returned by MCP tools or emitted in logs. Presence of a token
is not proof of admin or repository creation rights. The SSH fallback remains
disabled when host key trust cannot be proved.

## Release acceptance — mandatory before publication
1. Targeted and full Go tests, go vet, and three-provider HTTP fake-server
   tests for authentication, create, privacy, ambiguous POST, input validation,
   redirect refusal and Git-history secret detection.
2. A passing sealed CI run for the exact protected main commit and verified
   paired ARM64 controller/broker artifacts.
3. Live E2E: create a private test repository through MCPshka on GitHub,
   confirm access/private status, push verified Lane history to its GitHub
   remote with unchanged GitVerse remote and identical HEAD SHA.
4. GitVerse and MosHub lifecycle/CI status checks; API restrictions explicitly
   marked unsupported rather than treated as passed.
5. Confirm revoked/insufficient credentials, rate limit/timeout, TLS/SSH
   errors and lost response remain HOLD without duplicate mutations.
6. Guarded paired deployment followed by independent production version,
   controller/broker hash, tunnel, neighboring services and rollback tests.
7. Only then issue_update MCP-184 to fixed and issue_accept with production
   release evidence. No automatic issue closure from file/commit creation.

## Rollback
Use the guarded paired MCPshka rollback. The previous release 0.11.9 must
remain available. Never manually replace the running controller or broker,
restart Lane, change network settings or force-push repositories.
