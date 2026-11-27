# MCPshka 0.1.103 — authenticated broker maintenance and passkey hardening

Version increment: **0.1.102 -> 0.1.103** only.

## What is fixed
- An already-migrated, unprivileged MCP controller refuses `broker_install` before consuming a one-use administrator approval.
- WebAuthn first enrollment uses exclusive creation of the root-only pending challenge record to prevent races.
- The service-user-owned candidate is never executed by root without a pinned SHA-256 and immutable root-owned snapshot.
- The new independent-root bootstrap retains the old broker, restarts only the broker, verifies both running MCPshka 0.1.103 and broker health, and automatically restores the old binary on failure.
- No generic sudo, unrestricted root shell, or mutation of Tailscale/Kopilka/DNS/HA is added.

## Pinned broker candidate
`/root/repos/mcpshka/bin/mcpshka-broker-0.1.103.candidate`
SHA-256: `2d8fdf811e9a9319287f5f963b7685c055e2f49c74c62fdca3b455c0e6594783`

## One-time independent administrative bootstrap
After production has confirmed 0.1.103 and the bootstrap script is reviewed, the owner executes **one** command from an independently authenticated root terminal. This is intentionally not an MCP privileged call because a service-user controller is not allowed to install a root broker.

The trusted admin command must first `install -o root -g root -m 0700` the script from the repository to a root-only path, verify the resulting copy's exact SHA-256 against the published release, and **only then** execute that copy. The script itself rechecks a root-only copied broker candidate against the pinned binary checksum, retains rollback, and verifies service health.

Do not run the script directly from the writable worktree. Do not grant generic root shell to MCPshka or disable its post-migration protection.

## Face ID HTTPS
The passkey-only HTTP listener at `127.0.0.1:8799` is **disabled until configured**. The root broker reads an exact HTTPS origin at `/etc/mcpshka/passkey-origin`, and the frontend reads matching `MCPSHKA_PASSKEY_ORIGIN`. The user clicks a ChatGPT link; passkey enrollment is performed in Apple Passwords through WebAuthn. One-time browser enrollment may require the existing TOTP code; the code is never sent to ChatGPT.

An externally reachable HTTPS origin requires its own secure route. According to official Tailscale documentation, Funnel may listen only on 443, 8443, or 10000, and changing Serve to Funnel on a port makes **the whole port public**. The installed host already uses these ports, so do **not** run `tailscale funnel` on the existing node without isolating the HTTPS frontend on another public hostname/node. A separate Tailscale node/Funnel or dedicated named tunnel is needed. The OpenAI MCP tunnel is not a public browser tunnel. Never expose 8765, SSH, Kopilka, or HA to the public Internet.

## Acceptance
Go tests; diff/checksum; commit; build candidate; guarded deploy/rollback; production verify; independent root broker install receipt; exact HTTPS origin + no-VPN external iPhone connectivity; browser passkey enrollment; task-specific test after Face ID. Until the last three succeed, Face ID is **not** production-ready.
