# Docker access

Docker access is brokered. Do not add the MCPshka service user to the Docker
group or expose the Docker socket to jobs: either grants host administration.

The persistent operator configuration is `/etc/mcpshka/mcpshka.env`:

```ini
MCPSHKA_DOCKER_READ_CONTAINERS=homeassistant,torrserver,mosquitto,zigbee2mqtt,go2rtc,matter-server,lampac
MCPSHKA_DOCKER_TERMINAL_CONTAINERS=
```

On H96 the current kernel has `CONFIG_SECURITY_LANDLOCK` disabled (the syscall
returns ENOSYS). Terminal admission remains disabled; enabling the terminal
requires a supported kernel and real sandbox/TTY/teardown acceptance. Do not
remove the sandbox check or enable the allowlist as a workaround.

Refresh the broker from the reviewed release using the administrative security
gate. The installer writes the exact names to root-owned `broker.json`; restarting
only the MCP frontend does not refresh that policy. An absent or empty list
denies access. No container discovery implicitly grants access to new containers.

The existing `docker_list`, `docker_status`, `docker_inspect`, `docker_logs`, and
`docker_resources` tools share this read scope. Inspect returns selected metadata,
not environment variables. Logs are untrusted data and can contain application
secrets; request the smallest necessary window.

Restart/start/stop remain a separate explicit mutation scope. Read permission
does not grant lifecycle mutations, container removal, mounts, privileged exec,
or an unrestricted Docker command.

Interactive tools are `docker_terminal_open`, `docker_terminal_io` and
`docker_terminal_status`. Opening requires the existing exact-payload
administrative approval gate; subsequent IO is bound to that authenticated owner.
IO actions are write, resize and close. Input is base64, capped at 2048 bytes;
output uses byte cursors and a 32-KiB rolling window. Ctrl+] requests shutdown.
Input/output are not included in audit records; never type credentials into a
terminal. Outputs are untrusted application data.

The official Engine exec/TTY transport starts a pinned static supervisor as UID
65533 with only KILL/SETUID/SETGID file capabilities. Before accepting input, the
broker checks the actual running executable hash and credentials through `/proc`
using a pidfd and fresh Docker exec identity. This requires the broker's scoped
CAP_SYS_PTRACE capability and the administrative broker-install security gate.
The shell uses a dedicated non-root UID from 60000–60007, no new privileges and
a Landlock read-only filesystem. No root-shell option is exposed. Configuration
changes belong in separately authorized SHA-guarded filesystem operations.

The hard shell envelope is eight tasks, 64 MiB address space per process and
20 CPU seconds per process; up to 512 MiB per session. Observed aggregate CPU
20 seconds/RSS 128 MiB trigger earlier shutdown. Output has a hard 1-MiB total
cap; the supervisor expires after 15 minutes. Filesystem mutation is denied.
These are diagnostics limits, not permission to launch background services.

Writer admission uses immutable container IDs; renaming a container cannot free
its scope. Creation and input intents are fsynced before dispatch. A lost response
remains UNKNOWN and is not replayed. Disconnecting a Docker exec client does not
prove termination. Only a previously verified supervisor's successful exit after
kernel child drainage marks a session fenced; failures retain HOLD and its UID
slot. After a broker restart, an active terminal cannot be reattached: wait for
bounded expiry and reconcile. Do not start a replacement against an occupied ID.

## Bounded container self-healing (P15, first slice)

The controller checks only `MCPSHKA_DOCKER_RESTART_CONTAINERS` every 60 seconds.
It uses the same `dockerops` instance, mutation lock and broker restart scope as
`docker_action` restart. Read scope must also permit these containers. It never changes
Docker restart policies, container configuration, the network or services.
An empty restart allowlist performs no repairs.

| Environment variable | Default | Accepted values |
| --- | --- | --- |
| `MCPSHKA_SELF_HEAL_ENABLED` | `true` | Go boolean (`true`/`false`, `1`/`0`) |
| `MCPSHKA_SELF_HEAL_UNHEALTHY_MINUTES` | `5` | 1–1440 minutes |
| `MCPSHKA_SELF_HEAL_VERIFY_MINUTES` | `5` | 1–60 minutes |
| `MCPSHKA_SELF_HEAL_MAX_REPAIRS` | `2` | 1–10 attempts per container per hour |

Invalid configuration rejects daemon startup. Disabling self-healing preserves
its history. Configuration is read at controller startup.

A running, non-restarting container qualifies after continuously observed
`health=unhealthy` for the configured threshold. The first observation starts the
timer; missed observations longer than 150 seconds, read failures or clock
rollback reset it. A nonzero `exited` container qualifies immediately only with
an explicit restart policy other than `no`. Clean exits, empty/`no` restart
policies on stopped containers and Docker's own restart loops are left alone.

For host-networked `go2rtc`, the same loop also checks the primary private IPv4
address on the lowest-metric active default-route interface. The broker reads
`/proc/net/udp` and the go2rtc PID's socket inodes: a wildcard, Tailscale or
another process's socket does not count as `LAN-IP:5353`. A missing socket
qualifies two minutes after container startup; all go2rtc repairs have a durable
15-minute cooldown, in addition to the hourly limit and one-attempt-per-episode
rule. Recovery requires the LAN socket, even if Docker reports healthy.
Missing LAN addresses, unreadable process evidence and changed PID/start identity
stay unknown and cannot clear a failure or prove recovery.

Enable this with the existing self-heal switch and `go2rtc` in both Docker read
and restart allowlists; no new service or setting is needed. The bounded
`docker mdns go2rtc` broker observation is read-only and permits no other target.
Persistent mDNS failures appear as critical `container-mdns/go2rtc` findings in
`host_triage`; the existing issue sentinel files a card after two 15-minute
passes. A successful repair remains in self-heal incidents and the audit log.

Each failure fingerprint includes immutable container ID, name, state, health,
exit code, start/finish timestamps and restart policy. A repair intent and hourly
attempt are saved to the canonical SQLite state store before calling Docker.
Concurrent stale state cannot reserve another attempt. A process restart or
lost reply never replays a reserved Docker restart. Attempts include failed or
uncertain calls, so failures cannot bypass the hourly limit.

Later passes verify `running` and `health=healthy` within the configured deadline.
A missing healthcheck cannot prove success. Restart errors, uncertain results,
verification timeout or hourly limit exhaustion escalate and block subsequent
automatic mutations for that container, even if its fingerprint changes or the
hour expires. Observing `running` and `healthy` clears the escalation; the hourly
attempt history remains. The owner investigates an escalation before deciding
on manual intervention. No timer expiry clears an escalation; this slice provides no manual reset tool.

`self_heal_status` reads SQLite only: enabled/active configuration, exact restart
allowlist, fingerprints, verification deadlines, repair results and limits.
Container histories are paged four at a time with `cursor`/`next_cursor` and
`truncated`. Each container retains its latest 32 attempts, enough to enforce
the capped hourly limit; the latest 128 incident transitions are retained.
At most 128 distinct container histories are retained; exhausting this capacity
fails closed without deleting history.
Each transition also emits a `self_heal` audit event with time, container,
fingerprint and result. Audit failure prevents a new repair. SQLite or audit
errors from a pass are reported as `self_heal_pass` audit errors when possible.
`host_triage.last_self_heal` and the health snapshot's containers evidence show
the latest transition and expose pending escalations. Status/health reads do
not start the repair loop; the loop starts once with the daemon and drains on
shutdown. Each pass has a 50-second budget and runs serially.

Deployment acceptance should observe a controlled failure in an isolated test
container with a real healthcheck. Do not induce a Zigbee/HA outage to test it.
