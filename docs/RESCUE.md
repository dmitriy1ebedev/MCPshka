# Independent MCPshka recovery

`mcpshka-rescue.service` is a bounded external one-shot check driven by a 15-second
systemd timer. It is independent of the main process and LANE. Healthy checks
only inspect fixed local health/ready endpoints. The watcher restores a missing
or unready tunnel while main is healthy; it never fights a deliberately stopped
main, replays jobs or edits routing, DNS, VPN or unrelated units.

The deployment lock covers reconnect/restart and their acceptance. A watchdog
check skips an active guarded operation. Explicit restart rejects active or
unresolved job metadata. The existing on-failure main restart remains owned by
systemd; an unhealthy main is reported for reconciliation.

MCP tools `rescue_status` and `rescue_start` expose fixed modes only: reconnect,
restart, rollback. Worker/watch entry points, arbitrary services, shell commands
and paths are not broker capabilities. Recovery receipts use SQLite `rescues/`;
missing response or an inactive unfinished worker is UNKNOWN, never auto-replayed.
Rollback delegates to guarded deploy-self and requires the retained candidate's
hash to equal the installed previous binary. Scheduled rollback is not accepted
until project_deploy_status and production verification complete.

No OOB path is invented from online Tailscale peers. This release reports zero
verified independent OOB paths. P4 remains IN PROGRESS until two different VPS
management paths are actually verified and their combined failure recovery is
accepted. P0 VPS acceptance stays deferred. A healthy local watchdog is not proof
of remote OOB management.

Read-only netlink discovery is allowed in the main process with empty capability
sets. The root broker retains the previously accepted AF_PACKET, CAP_NET_ADMIN and
CAP_NET_RAW allowlist needed for the bounded DIAG traffic-capture helper; P4 does
not grant any additional broker authority. Main/broker NoNewPrivileges and
UID/MainPID-bound broker authentication remain.

Isolated process tests kill three test main/tunnel processes, recover their
control path, reject restart with active jobs, and honor an active deployment
lock. They do not stop production or neighboring services and do not establish
real VPS OOB acceptance.
