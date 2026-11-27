# MCPshka 0.1.109 — versioned privileged task approval

Status: installed on H96 on 2026-10-15. Guarded deployment `1792034585755-3f92f64c5002` succeeded in 27 seconds; production verification passed. Physical owner WebAuthn acceptance remains pending.

- Privileged task approvals pin plan version 1, the owner, scope, task ID and reviewed original/staged content hashes.
- A newer approval fences earlier permits for the same HA configuration. Approval issuance, apply and rollback share a resource lock.
- Receipts identify WebAuthn or TOTP authorization and its timestamp. Historical receipts remain readable; unversioned permits cannot start new mutations.
- The MCP frontend checks broker protocol compatibility before privileged actions. The owner page displays the plan version and operation.
- Verified WebAuthn completion continues directly into the existing bounded task execution path, without a second chat command. TOTP, backup and guarded rollback remain supported.

The compatible broker must be installed through the independently authorized operator path before this frontend. The fixed binary source is `d90e1a21507f6539a6f192188061ec3745f83931`. All four mandatory RELEASE checks passed locally (1145.543 seconds) and in GitHub Actions run 38670041198 (242.472 seconds); the downloaded artifact digest was verified. Two builds of each ARM64 binary matched. Production and rollback identities were verified. Actual owner WebAuthn and automatic HA execution are still open acceptance work; unit tests do not prove them.

The initial privileged scope remains Home Assistant automations.yaml. Applying the file reports reload_required; Home Assistant reload is a separate action. No generic root shell, arbitrary operation catalog, public MCP endpoint or cloud production credential is introduced.

Operational maintenance is committed separately in `42f5aa2`: a fixed SHA-pinned broker updater and HTTP-before-tunnel startup ordering. All 73 native tests passed, including nine updater tests; merged main rootbroker and MCP server tests passed. An initial startup-race deployment rolled back to 108 successfully before the corrected deployment succeeded. See [broker maintenance](../history/BROKER_UPGRADE_109.md).

Installed agent SHA-256: `8a24658c67242f439abde6d64db00e6bb2254a64ef70ede643da5f539c3e415c`. Rollback 108 SHA-256: `d3362d32083d28ddbbece2704b3f4d2da67dca3a8d581ac277e0d6d59d21f8c1`. Broker SHA-256: `03fa67e1a2cb873f3aada0957b334a922e8be22a3808a7eb13c336f10fcd0aba`.
