# Документация MCPshka

Где что лежит. Правило одно: **действующее — в `docs/`, релизы — в `docs/releases/`, история — в `docs/history/`, план и журнал — в `ROADMAP.md` + `roadmap/` (локально, вне git).**

| Место | Что | В git |
|---|---|---|
| `ROADMAP.md` (корень репо) | План по фазам P0–P31 и живой журнал: релизы, реестр багов `BUG-ГГГГММДД-N`, очередь версий. Путь закреплён кодом (`roadmapscan`, integration queue, workflow, `project_context` пинят его SHA) — не переносить. | нет |
| `roadmap/EVIDENCE_LOG.md` | Подробный журнал свидетельств (SHA, CI run, job ID, замеры), новые записи сверху | нет |
| `roadmap/REVIEW.md` | Ревью roadmap с предложениями (не policy и не приёмка) | нет |
| `roadmap/AI_COORDINATION.md` | Правила координации AI-исполнителей | нет |
| `docs/*.md` | Действующие руководства и контракты (список ниже) | да |
| `docs/releases/RELEASE_<версия>.md` | Версионированные заметки; актуальный production определяет `release_apply_status` + `project_verify`, а не имя последнего файла | да |
| `docs/history/` | Датированные отчёты, ревью, интеграции фаз, session-recovery — только для справки | да |
| `docs/archive/` | Исторические срезы issue-registry и устаревшие аудиты; исходные данные сохранены, но не являются актуальным статусом | да |

## Как вести

1. Новый релиз → `docs/releases/RELEASE_<версия>.md` + строка в «Релизах» `ROADMAP.md` + запись сверху в `roadmap/EVIDENCE_LOG.md`.
2. Найден дефект → `issue_list` / `issue_create` / `issue_update` в MCPshka; приёмка закрытия только через `issue_accept` с проверенным evidence. Исторические Markdown-срезы в `docs/archive/` — не источник актуального статуса.
3. Документ перестал быть действующим (отчёт, разовая процедура) → `git mv` в `docs/history/`.

## Действующие документы

| Файл | Тема |
|---|---|
| [ADMIN_LEASE_RECOVERY.md](ADMIN_LEASE_RECOVERY.md) | Administrative main-repository lease recovery |
| [BUGS.md](BUGS.md) | Баги: как распознать, завести, закрыть; готовые диагнозы |
| [CAPABILITIES.md](CAPABILITIES.md) | Capability discovery |
| [CLIENT_COMPATIBILITY.md](CLIENT_COMPATIBILITY.md) | Client compatibility |
| [CLOUD_AI_AGENTS.md](CLOUD_AI_AGENTS.md) | Cloud AI workers for MCPshka: explicit trust and deployment boundaries |
| [CLOUD_DEVELOPMENT.md](CLOUD_DEVELOPMENT.md) | Cloud development — 0.1.108 |
| [CLOUD_TASK_JOURNAL.md](CLOUD_TASK_JOURNAL.md) | Durable cloud task journal — staged acceptance |
| [CODEX_INTEGRATION.md](CODEX_INTEGRATION.md) | Local Codex integration |
| [DISTRIBUTED_CI.md](DISTRIBUTED_CI.md) | Distributed CI and unified release |
| [DOCKER_ACCESS.md](DOCKER_ACCESS.md) | Docker access |
| [EXECUTOR_RUNTIME.md](EXECUTOR_RUNTIME.md) | Isolated executor runtime |
| [GITVERSE_GIGAIDE.md](GITVERSE_GIGAIDE.md) | GitVerse immutable snapshots and GigaIDE Cloud lane |
| [GITVERSE_SNAPSHOT_CI.md](GITVERSE_SNAPSHOT_CI.md) | GitVerse snapshot quick-check admission |
| [HOST_LIFECYCLE.md](HOST_LIFECYCLE.md) | Host lifecycle and filesystem migration |
| [INTERACTION_RECOVERY_DIAGNOSIS.md](INTERACTION_RECOVERY_DIAGNOSIS.md) | Bounded interaction recovery diagnosis |
| [LEASE_HEARTBEAT_P7.md](LEASE_HEARTBEAT_P7.md) | P7 — tracked project lease heartbeat (2026-10-08) |
| [LEASE_RECOVERY_HOLD.md](LEASE_RECOVERY_HOLD.md) | Lease recovery: required evidence before activation |
| [MANAGED_WORKFLOWS.md](MANAGED_WORKFLOWS.md) | Managed bounded workflows |
| [MCP_ARTIFACT_INGRESS.md](MCP_ARTIFACT_INGRESS.md) | MCP artifact inbox |
| [OPEN_SOURCE_RESEARCH.md](OPEN_SOURCE_RESEARCH.md) | Open-source research for MCPshka |
| [R3_ADMISSION_PLANNER.md](R3_ADMISSION_PLANNER.md) | R3 offline admission planner (non-executable) |
| [RELEASE_PROMOTION.md](RELEASE_PROMOTION.md) | Signed release admission through MCP |
| [RESCUE.md](RESCUE.md) | Independent MCPshka recovery |
| [ROOT_TASK_TOTP.md](ROOT_TASK_TOTP.md) | One code per privileged task (Apple Passwords TOTP) |
| [SECURE_MCP_TUNNEL.md](SECURE_MCP_TUNNEL.md) | OpenAI Secure MCP Tunnel for MCPshka |
| [TESTING_TIERS.md](TESTING_TIERS.md) | Bounded test tiers |
| [TOOL_CONTRACTS.md](TOOL_CONTRACTS.md) | Mutation contracts |
| [UPDATER_HELPERS_REFRESH.md](UPDATER_HELPERS_REFRESH.md) | Refreshing root updater helpers (0.1.153) |
| [VERSIONING.md](VERSIONING.md) | Версии MCPshka |
| [WORK_SEGMENT_COMPLETION.md](WORK_SEGMENT_COMPLETION.md) | Voluntary work-segment completion |
| [github-ssh-deploy.md](github-ssh-deploy.md) | GitHub SSH deploy-key integration (isolated) |
