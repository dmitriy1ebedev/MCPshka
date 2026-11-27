# MCPshka 0.1.102 — passkey approval pilot

Version policy: 0.1.101 -> 0.1.102. No suffix or tier reset.

## Scope
- Optional task-scoped Apple passkey (WebAuthn / Face ID) confirmation for Home Assistant automations.
- Separate root-broker verification. MCP frontend remains the mcpshka service user.
- Each ceremony is bound to the authenticated MCP owner, 128-bit task ID, and production/staged SHA-256.
- TOTP is retained for one-time browser enrollment and as a recovery mechanism.
- In an enrolled deployment, Face ID approval applies the pinned YAML, creates a root-owned backup, and reports that Home Assistant automation reload is required.
- The approval is short-lived; no general-purpose root shell or Docker permission is unlocked.
- Default disabled unless the root-owned HTTPS origin and an explicitly configured HTTPS reverse proxy are present.

## HTTPS setup (separate controlled infrastructure change)
The broker reads its RP origin from /etc/mcpshka/passkey-origin (root-only).
The MCP frontend uses MCPSHKA_PASSKEY_ORIGIN with the exact same HTTPS origin.
Expose only /approval/ and /passkey/enroll paths via a dedicated origin and trusted TLS. Do not make the MCP API or private port public.
Do not reuse the Kopilka financial RP hostname; each app should have a separate RP and isolated browser origin.
Do not run passkey enrollment on plaintext HTTP or an untrusted certificate.
Preferred convenience architecture: Tailscale **Funnel** on port 8443, not Tailscale Serve (Serve requires iPhone tailnet access). Funnel is publicly reachable without an iPhone VPN. Use the fixed `*.ts.net` hostname and HTTPS origin with `:8443`; WebAuthn origins include the port. Funnel must be enabled in tailnet policy with MagicDNS and HTTPS enabled.
The passkey-only backend uses 127.0.0.1:8799 and serves no `/mcp`, `/artifacts` or `/healthz`. Example after independently approving infrastructure changes: `tailscale funnel --bg --https=8443 http://127.0.0.1:8799`. Never use this command against port 8765. Do not overwrite the Kopilka Serve 443 configuration.
MCPshka must have MCPSHKA_PASSKEY_ORIGIN set to the exact public origin; the broker reads the same root-owned value from /etc/mcpshka/passkey-origin. The portal listener is entirely absent by default.
The user taps a confirmation link in ChatGPT, confirms Face ID in the iPhone's system browser, and the appliance applies the pinned change; they never switch Tailscale VPN or use Termius. ChatGPT cannot natively invoke Face ID inside its own chat without platform support.
The HTTPS forwarding, tailnet policy and service environment must be provisioned independently under existing network-change policies.

## Tests
Go tests for all packages, secure-origin and host validation, rejected cross-origin requests,
broker challenge origin/UV requirements, SHA pinning, one-time permits, successful rollback,
and end-to-end browser checks are acceptance requirements for enabling the feature.
Do not claim production Face ID readiness until actual iPhone enrollment and approval succeed.
