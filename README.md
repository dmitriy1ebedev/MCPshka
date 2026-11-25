# MCPshka

MCPshka is a small remote-control agent for AI clients and personal automation.

The goal is not to clone a general remote desktop product. The goal is to provide a predictable, bounded and auditable control plane for Linux hosts, especially small always-on ARM64 systems.


## Текущий выпуск: 0.19.1 (2026-11-25)

- Открытое ядро на GitHub под Apache-2.0. MCP-сервер на Go для ИИ-клиентов (ChatGPT, Claude, GigaChat и другие): ограниченный, проверяемый и журналируемый доступ к Linux-хосту, проектам, CI, выпуску и дому. Разработку ведёт ИИ инструментами MCPshka. Опасное подтверждается Face ID владельца, тяжёлое идёт через очередь.
- Что менялось — [CHANGELOG.md](CHANGELOG.md); баги и причины — [BUGLOG.md](BUGLOG.md); выпуски — `docs/releases/`; правила работы ИИ — `mcpshka_guide topic=rules`.
- Открытое ядро выгружается инструментом `opencore_publish` из канонического main: секреты, коннекторы и домашние адреса владельца в публичное дерево не попадают (`.mcpshka/opencore.json`).
- Разделы ниже про 0.1.x — история архитектуры.

## Чем вдохновлялись

MCPshka выросла из чужих хороших идей; спасибо авторам. Ниже — откуда что взято.

- [Desktop Commander MCP](https://github.com/wonderwhy-er/DesktopCommanderMCP) (MIT) — образец «пульта» для ИИ-чата: сессии процессов с частичным выводом, чтение файлов окнами, точная правка блоков, поиск сессиями, Run/Cancel прямо в чате. Набор `rdc` повторяет его имена и смысл.
- [Model Context Protocol](https://modelcontextprotocol.io) и [официальный Go SDK](https://github.com/modelcontextprotocol/go-sdk) — протокол и транспорт сервера.
- [Home Assistant](https://github.com/home-assistant/core) — дом под одним пультом; MCPshka управляет им как провайдером и хочет быть таким же «домашним центром», только для ИИ-агентов.
- [Zuul: project gating](https://zuul-ci.org/docs/zuul/latest/gating.html) и merge queue — очередь вливания с упреждающими прогонами; основа распределённого CI на четырёх площадках.
- [Temporal](https://github.com/temporalio/sdk-go) и [go-workflows](https://github.com/cschleiden/go-workflows) — долговечные процессы: работа как журналируемая задача, которую можно продолжить после сбоя.
- [Litestream](https://github.com/benbjohnson/litestream) и [age](https://github.com/FiloSottile/age) — резервные копии SQLite и шифрование бэкапов.
- [tusd](https://github.com/tus/tusd) — докачиваемая передача больших файлов вне ответа JSON-RPC.
- [sony/gobreaker](https://github.com/sony/gobreaker) — предохранитель при недоступном брокере.
- [OpenTelemetry Go](https://github.com/open-telemetry/opentelemetry-go) — словарь трассировки и метрик.
- [OpenAI Codex CLI](https://github.com/openai/codex) — агент-исполнитель задач, которым MCPshka умеет управлять.
- [freellmapi](https://github.com/tashfeenahmed/freellmapi) — шлюз бесплатных LLM-провайдеров, основа FreeLLM.
- Инструменты [Uncle Bob](https://github.com/unclebob) ([crapper](https://github.com/unclebob/crapper), [mutator](https://github.com/unclebob/mutator), [dryer](https://github.com/unclebob/dryer)) — метрики сложности, мутационное тестирование и поиск дублей для самопроверки кода.

## Design goals

- One small native Go binary.
- MCP is an adapter, not the application core.
- Long-running work always becomes a job.
- Full stdout/stderr stays on the controlled host.
- Model-facing output is cursor-based and server-capped.
- Direct argv execution is the default; no implicit shell parsing.
- Explicit filesystem roots and execution policy.
- Process-group cancellation to avoid orphan children.
- Live process-group CPU/RSS/I/O/member observations for bounded anti-hang decisions.
- Local audit trail with secret redaction.
- Low idle CPU/RAM usage.
- Native installation first; Docker remains an optional isolation mode.
- Linux and Astra Linux are primary targets.
- Remote transport must not require opening an inbound router port.

## Legacy lease administrative recovery (0.1.133 candidate)

Normal release remains `project_lease_release(path, token, idempotency_key)` by the existing holder with the current secret token. Age, missing heartbeats, a chat administrator's consent, or a supposedly idle session are **not** a safe takeover proof.

A root-owned, Face-ID-authorized recovery path for an **unbound, execution-tracked** legacy lease uses `project_lease_admin_recovery_preview(session_id, path)`, `project_lease_admin_recovery_status(session_id, path)`, and `project_lease_admin_recover(session_id, path, plan, idempotency_key)`. Preview pins the lease's identity, record SHA-256, current Git tree, common Git directory and a unique immutable audit journal ID. Recovery rejects a dirty repository, changed lease identity, live process references, unsupported/unknown process evidence, missing root-owned 0500 fences, and inactive or foreign Face ID sessions. The privileged root broker performs a process re-check, writes a closed writer-admission gate, then atomically rotates the holder's token and generation together with an immutable SQLite audit receipt; an old token cannot heartbeat or release the successor.

The root operator must **independently** prepare and retain a sealed source and shared-Git boundary before recovery and must reconcile/deploy/open the gate in the existing authorized maintenance workflow. The new holder is an operator-root maintenance identity, not an automatically activated interactive writer. A failed or lost reply is UNKNOWN: inspect the lease and audit records; do not retry with a new plan. Managed PID-bound leases and legacy records without the required execution identity remain HOLD. This does not grant the AI arbitrary root permissions or bypass broker installation/security gates.

## Current alpha: 0.1.82

Implemented:

- device_status MCP tool
- exec_start MCP tool
- job_status MCP tool
- job_output MCP tool
- job_cancel MCP tool
- on-device stdout/stderr files
- 16 KiB default / 64 KiB hard read cap
- process groups with TERM -> KILL cancellation
- graceful MCPshka shutdown cancels and reaps active process groups before exit
- new jobs are atomically rejected once shutdown begins
- 32 MiB default combined stdout/stderr disk quota per job (64 MiB hard cap)
- 30 minute default wall-clock timeout per job (6 hour hard cap)
- default per-job resource ceilings: 1 GiB aggregate RSS, 30 minutes aggregate CPU time, 64 descendant processes
- hard resource caps exposed by exec_start: 2 GiB RSS, 6 hours CPU time and 256 processes
- resource watchdog terminates the full process group and records the exact limit violation
- production jobs run in per-job systemd transient scopes with MemoryMax, CPUQuota and TasksMax cgroup ceilings; scope use can be disabled with MCPSHKA_JOB_SCOPES=off
- job status reads aggregate cgroup CPU/memory/I/O/process evidence when a scope is active, with process-group fallback
- cancellation, timeouts and shutdown terminate both the transient scope and the wrapper process group
- maximum 4 concurrent jobs
- dropped-output accounting without blocking the child process
- isolated job HOME/cache under MCPshka state instead of /root
- default working-directory roots
- exec_classify preflight for read_only / mutating / destructive / privileged commands
- default execution policy allows read_only/mutating and rejects destructive/privileged classes
- built-in reader/operator role profiles; profiles can only narrow the global command-class ceiling
- optional per-project role overrides resolved by canonical workdir, with most-specific project root winning
- exec_classify can evaluate the effective role profile for a supplied workdir
- explicit denylist remains as defense in depth for machine power/filesystem formatting commands
- destructive commands remain blocked by default but can use a short-lived, one-time approval token bound to exact command, argv and canonical workdir
- approval_request and approval_status never expose a granted token; approval-grant is a separate privileged CLI action and tokens are stored only as hashes
- command class persisted in job metadata and audit records
- minimal child environment
- persistent job metadata with atomic writes
- restart recovery for interrupted jobs
- job_status exposes live process-group state, member count, CPU ticks, RSS and I/O bytes while a job is running
- JSONL audit trail with argument redaction and size rotation
- automatic TTL/reaper for old finished jobs
- bounded filesystem list/read/find tools
- optional ripgrep acceleration for content search with automatic pure-Go fallback
- atomic hash-guarded fs_patch
- symlink-safe filesystem containment
- stdio MCP transport through the official MCP Go SDK
- authenticated Streamable HTTP MCP bound to loopback
- native MCP 2026-07-28 discovery and stateless Streamable HTTP via the official Go SDK v1.8.0, with legacy revisions retained for compatibility
- health/version endpoint for the HTTP transport
- bounded structured git_status and git_diff tools
- project_detect for Git/language/build markers without executing project code
- project_test planner for Makefile, Node, Go, Rust and Python projects
- project_test launches through the normal bounded job manager with no shell-string interpretation
- project_build planner for Makefile, Node, Go and Rust projects
- project_build launches through the same bounded job manager without shell-string interpretation
- project_profile resolves reusable bounded workflow profiles; DIAG has a built-in test/build/check profile
- project_run launches a named profile workflow through the normal policy/job/idempotency path; caller limits can only tighten profile limits
- workflow_start/status/resume coordinate trusted ten-stage profile pipelines with durable worktree claims, bounded jobs, compensation and receipt-based recovery; see docs/MANAGED_WORKFLOWS.md for limits and remaining P7 acceptance
- repositories can override or add a profile with a strictly validated non-symlink .mcpshka/profile.json using schema mcpshka.project-profile/v1
- project_deploy provides a pinned MCPshka self-deploy profile with required idempotency, detached transient execution, atomic rollback preservation and durable deploy status
- project_deploy_status reads bounded durable deployment state after MCP reconnects
- project_cleanup_candidates plans then fingerprint-guards cleanup of obsolete candidate builds while preserving production and rollback candidates
- project_verify performs bounded post-deploy checks for Git cleanliness, installed/rollback versions, services, health/ready endpoints, DNS and outbound HTTPS
- verify-production CLI exposes the same production verification locally and exits nonzero when any check fails
- generic systemd-run execution is classified privileged; self-deploy uses only the dedicated guarded path
- project_lease_acquire/status/release enforce an atomic single-writer lease per canonical Git worktree root
- lease release requires the secret token returned only to the successful acquirer; status never exposes it
- durable idempotency keys protect exec_start, fs_patch, project_test/build and project lease mutations from duplicate transport retries
- idempotency_status exposes PENDING / COMPLETED / FAILED state; PENDING is treated as unknown outcome rather than permission to replay
- idempotency records store request fingerprints and terminal results on the controlled host so retries survive MCP reconnects and daemon restarts
- artifact_get short-lived tickets for large files without placing bytes in model context
- authenticated streaming artifact downloads with HTTP Range and stale-file rejection
- native status and version CLI commands
- service_list/service_status return bounded structured state for allowlisted systemd services, including exit/result fields useful for failed-reason analysis
- service_dependencies returns bounded Requires/Wants/After/Before relationships and preserves non-service dependency units such as targets and sockets
- journal_read adds cursor-resumable bounded journal reads for allowlisted services with capped lookback, lines and bytes
- service_action provides idempotent audited start/stop/restart only for MCPSHKA_SYSTEMD_MUTATION_SERVICES; the legacy restart allowlist grants restart only
- service_action action=restart also covers MCPSHKA_SYSTEMD_RESTART_SERVICES (service_restart and service_logs were removed in 0.11.3); mcpshka.service self-mutation remains reserved for guarded self-deploy
- systemd read, restart-only and mutation allowlists are configured independently with MCPSHKA_SYSTEMD_SERVICES, MCPSHKA_SYSTEMD_RESTART_SERVICES and MCPSHKA_SYSTEMD_MUTATION_SERVICES
- docker_list/docker_status expose bounded compact container state; docker_logs has hard time/line/byte caps
- docker_inspect reports bounded image/restart/Compose/network/mount metadata and docker_resources returns one bounded no-stream resource snapshot
- broker Docker reads require an explicit MCPSHKA_DOCKER_READ_CONTAINERS list; logs, inspect, stats and status expose only exact listed names. Restart/mutation permissions do not grant reads. An empty list denies Docker reads. Refresh the root-owned broker configuration through the approved installer before activation.
- docker_action provides idempotent audited start/stop/restart only for MCPSHKA_DOCKER_MUTATION_CONTAINERS; destructive remove/kill operations are not exposed
- docker_action action=restart also covers MCPSHKA_DOCKER_RESTART_CONTAINERS (docker_restart was removed in 0.11.3); the restart-only allowlist does not grant start/stop
- network_snapshot returns bounded links/addresses, routes, resolver configuration, listening TCP/UDP sockets, VPN interface awareness and a stable state fingerprint using fixed read-only commands
- network_connectivity runs fixed-target bounded DNS and HTTPS probes; it accepts no arbitrary destination from the caller
- traffic_route_start/status/report bridge to the root-owned DIAG traffic observer through the PID-bound broker: explicit client IPv4+MAC, 10..900 second bounded sessions, fixed `/var/lib/diag/traffic-runs`, idempotent start, no generic privileged shell or network mutation
- VPN awareness identifies Tailscale, WireGuard and generic tunnel interfaces without reading private VPN keys
- control_path_status identifies the active outbound MCP path, transport-only fallbacks, verified backups, bounded Tailscale peer summaries (host/DNS/IP/online/active/exit-node only; no node keys), LANE authority, rollback readiness and emergency state
- mutation_preflight assesses blast radius and blocks loss of the only verified management path; LANE-managed network mutations must be provider-owned, staged and rollback-bounded
- generic direct network-control CLIs (ip/route/ifconfig/tc/ethtool/nmcli/networkctl/resolvectl/tailscale/wg/wg-quick/openvpn) are privileged by default; normal inspection uses bounded dedicated tools and persistent writes use LANE
- bounded local LANE provider uses the trusted `/run/lane/api.sock` business API instead of duplicating routes/firewall/VPN writers
- network_stage_apply requires explicit affected management paths and idempotency, captures compact before/after network state, then creates/applies only a LANE plan that returns `PENDING_COMMIT` with an armed 30..900 second rollback timer
- network_stage_commit verifies both live LANE state and the MCPshka control path before LANE disarms rollback; network_stage_rollback remains an explicit recovery path
- hardened systemd service packaging with atomic install, uninstall and rollback
- production systemd deployment on the H96 Max X3
- pinned official OpenAI tunnel-client packaging for ARM64/AMD64 with SHA-256 verification
- prepared outbound-only Secure MCP Tunnel profile with root-only local bearer handoff
- live outbound-only OpenAI Secure MCP Tunnel with ChatGPT personal-plugin end-to-end validation

MCPshka is deployed on the H96 Max X3 and validated end to end from ChatGPT through the OpenAI Secure MCP Tunnel. Production migration from the legacy identity is handled by the guarded migration scripts in packaging/native.

## Commands

    mcpshka version
    mcpshka status
    mcpshka control-status
    mcpshka lane-status
    mcpshka verify-production
    mcpshka deploy-self <candidate_path> <expected_version>
    mcpshka cleanup-candidates [expected_fingerprint]
    mcpshka approval-grant <approval_id>
    mcpshka mcp-stdio
    MCPSHKA_TOKEN=... mcpshka mcp-http

## HTTP authentication

For production HTTP transport, prefer `MCPSHKA_AUTH_FILE` over a single shared bearer token. The registry is a regular non-symlink JSON file with mode `0600`; it stores identity IDs and SHA-256 token digests, not plaintext tokens. Registry entries may be added for rotation, revoked, or given an expiry time, and the registry is re-read on authentication so rotation/revocation does not require an MCPshka restart. Each credential may also declare `role` (`reader` or `operator`) and exact MCP tool `scopes`; `*` grants all tool scopes. For backward compatibility, omitted role/scopes normalize to `operator` + `*`.

If `MCPSHKA_AUTH_FILE` is unset, the legacy `MCPSHKA_TOKEN` bearer remains supported for compatibility. If neither is configured, `mcp-http` fails closed before listening. When a registry is configured, the legacy token cannot bypass it.

## Policy configuration

The default role is `operator`, which preserves the global read_only/mutating ceiling. Set `MCPSHKA_DEFAULT_ROLE_PROFILE=reader` to make the whole agent read-only by default.

Per-project overrides are supplied as a JSON object in `MCPSHKA_PROJECT_PROFILES`, for example:

    MCPSHKA_PROJECT_PROFILES='{"/root/repos/diag":"reader","/root/repos/mcpshka":"operator"}'

Project paths are resolved through the same allowed-root and symlink checks as job workdirs. An override never enables a command class that the global policy already rejects.

## Project workflow profiles

Reusable project workflows use schema `mcpshka.project-profile/v1`. MCPshka includes a built-in `diag` profile matched by the DIAG Go module and Makefile, with bounded `test`, `build` and `check` workflows. A repository can provide its own regular, non-symlink `.mcpshka/profile.json`; repository profiles take precedence after strict schema and project-match validation.

Example:

    {
      "schema": "mcpshka.project-profile/v1",
      "name": "example-go",
      "match": {"module": "example.test/app", "markers": ["go.mod"]},
      "workflows": {
        "test": {
          "command": "go",
          "args": ["test", "./..."],
          "timeout_seconds": 600,
          "output_limit_bytes": 8388608
        }
      }
    }

Profile commands are direct executable+argv definitions, not shell strings. They still pass through the normal command-class policy, and `project_run` cannot raise the profile's timeout or output budget.

## Repository layout

    cmd/mcpshka          CLI entry point
    internal/jobs         asynchronous process lifecycle and bounded output
    internal/laneprovider  bounded authoritative LANE provider adapter
    internal/networkops    bounded read-only links/routes/DNS/socket snapshots
    internal/approval     durable one-time destructive-operation approvals
    internal/artifacts    short-lived streaming download tickets
    internal/audit        rotated JSONL audit trail and redaction
    internal/deploy       guarded self-deployment scheduling and durable status
    internal/dockerops    bounded Docker list/status/logs and allowlisted restart
    internal/filesystem   bounded list/read/search and atomic guarded patching
    internal/gitops       bounded Git status/diff operations
    internal/idempotency  durable retry-key state and replay protection
    internal/project      project detection without executing project code
    internal/projectprofile reusable validated project workflow profiles
    internal/projectlease atomic single-writer project/worktree leases
    internal/policy       execution/workdir and filesystem path policy
    internal/systemdsvc    allowlisted bounded systemd status/restart operations
    internal/systeminfo   compact host snapshot
    internal/verify       bounded post-deploy production verification
    internal/mcpserver    MCP adapter
    docs                  architecture and research notes

## Core rule

No tool is allowed to dump an unbounded command result into model context.

A command returns a job identifier. Output is read separately with a byte cursor and a hard server-side limit.

## Security status

The HTTP listener stays on loopback with a bearer token; remote clients reach it through an authenticated tunnel (OAuth), and owner-only actions need a WebAuthn (Face ID) approval.

Command classes are risk routing, not a sandbox: an allowed interpreter or build tool can still execute arbitrary code. Destructive commands now require a separate one-time approval capability, while explicitly denied commands remain unapprovable. The project still needs a dedicated service identity/sandbox boundary and OAuth-capable authentication before wider remote deployment.

Claude Free is an explicit compatibility target through its single custom remote MCP connector; see docs/CLIENT_COMPATIBILITY.md.

See docs/OPEN_SOURCE_RESEARCH.md and ROADMAP.md.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
