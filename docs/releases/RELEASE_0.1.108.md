# MCPshka 0.1.108 — isolated cloud checks

Heavy release checks can run in the private GitHub repository while H96 retains production policy and guarded deployment. This release preserves the complete RELEASE gate.

- A shared GitHub/GitVerse workflow pins the reviewed source and offers release, extended race, bounded fuzz and diagnostic cross-build suites with resource and artifact limits.
- Clean runners install a pinned Python test dependency outside the checkout.
- Structured cloud results verify source SHA, planned checks and exit codes; incomplete observations stay UNKNOWN and never authorize deployment.
- Test selection includes workflow dependency changes. Jobs test cleanup waits for persistence, and terminal fixtures explicitly preserve private directory permissions.

The fixed release source f6d24eb1c3bd929fac595ffc658de055e6b010b8 passed all four RELEASE checks in 101.051 seconds (GitHub run 38620360406); downloaded artifact 11921656997 passed digest and source verification. Two cached ARM64/CGO builds produced the same SHA-256 d3362d32083d28ddbbece2704b3f4d2da67dca3a8d581ac277e0d6d59d21f8c1. Guarded deploy 1792004978258-e75f20f78a7a succeeded in 29 seconds; production version/hash, health, tunnel, DNS, HTTPS and rollback 0.1.107 were verified. Protected Docker services stayed running. Details and immutable identities are in CLOUD_DEVELOPMENT.md.

GitHub RELEASE execution is verified. GitVerse new-run dispatch, GigaIDE authenticated execution and cloud Codex handoff remain unverified. Paid cloud agents are excluded. Details and evidence are in CLOUD_DEVELOPMENT.md. No production credentials or Tailscale identity are copied to cloud runners.
