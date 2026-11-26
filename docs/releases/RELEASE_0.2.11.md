# MCPshka 0.2.11 - verified Tailscale SSH backup management path

Date: 2026-11-02. Base: protected main c31ef72 (0.2.9). Version 0.2.10 stays reserved for the unshipped commit 8ad4cbe on claude/r20-030.

## Change
- `control_path_status` reports a new external path `tailscale_ssh` (kind `overlay_ssh`). Transport is ready when Tailscale is `Running` and `Self.Online`.
- It is management-ready and verified only from live host evidence:
  - an sshd listener on port 22 bound to 0.0.0.0, :: or this node's Tailscale IP (`/proc/net/tcp`, `/proc/net/tcp6`);
  - an ESTABLISHED TCP session to local port 22 whose remote is a Tailscale address (100.64.0.0/10 or fd7a:115c:a1e0::/48) of an online tailnet peer;
  - `ip -4 rule` order: `to 100.64.0.0/10 lookup 52` precedes every fwmark rule that could steer overlay replies elsewhere. Two kinds of preceding fwmark rules are tolerated: Tailscale's own bypass mark `0x80000/0xff0000` (stock rules 5210/5230/5250; tailscaled sets it only on its own WireGuard UDP), and rules whose table contains only `local` routes (TPROXY delivery, never egress; e.g. 5095 to table 177).
  - Otherwise `reason` names the first failed check; an unreadable socket table or failed `ip` call stays `unknown`.
- `mutation_preflight`: a verified `tailscale_ssh` outside `affected_paths` is a verified backup. A staged LANE firewall/routes change with `rollback_seconds` 30..900 that affects only `openai_tunnel` is GUARDED/allowed. It stays FORBIDDEN without the backup, or when `tailscale_ssh` or `tailscale` is affected.

## Not changed
- No new listening interfaces for the MCP API, and no Tailscale, SSH, LANE or network rule changes. The check is read-only.

## Checks
- Unit tests cover the /proc/net/tcp parser (v4, v6 and v4-mapped), rule order (live H96 order, foreign fwmark first, missing rule, unknown table) and preflight decisions (backup present, absent and affected; overlay affected; rollback bounds; non-LANE provider).

## Rollback
- The previous installed pair 0.2.9 is kept as `.previous` by the coordinated release.
