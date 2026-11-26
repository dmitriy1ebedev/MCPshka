# MCPshka 0.7.3 — исправления MCP-126/127/128/129/131

Дата: 2026-11-04. Ветка: `fix/mcp-126-131-20261104` (от `main`). Выпуск единый, приёмка требует CI, integration и guarded release.

## Изменения

- **MCP-126** — `HasTools` для полного и компактного MCP-серверов, in-process уведомление `notifications/tools/list_changed` и точная дельта имён/схем при `CLIENT_SCHEMA_STALE`. Невозможность заставить внешний клиент переинициализироваться после разрыва остаётся отдельным ограничением клиента.
- **MCP-127** — существующий единый потолок flexible timeout (`1800`, `1944`, `21600` разрешены, `21601` запрещён); regressions `internal/executor`.
- **MCP-128** — существующая вместимость Git proof 2 MiB; regression 3000 tracked файлов в `internal/projectlease`.
- **MCP-129** — обновление отдельного root-owned systemd drop-in ресурсов брокера (`CPUQuota=100%`, `CPUWeight=400`, `IOWeight=400`, `TasksMax=96`) в guarded coordinated-upgrade. Старые байты/отсутствие файла архивируются и восстанавливаются при rollback; `broker.json` не меняется. `project_verify` сверяет значения PID1.
- **MCP-131** — Face ID ссылки с кратчайшим уникальным префиксом от 8 hex, при коллизии удлиняются; full task ID остаётся в отдельном поле.
- **MCP-134** — проверка формы `phase_checkpoint_record` перед mutation receipt, чтобы ошибочный критерий не создавал `PENDING`.
- **MCP-136** — `PROJECT_BUSY` и «job admission in progress» (`jobs.WithIdleProject`) теперь `failure.RejectedAs` (no-effect): все вызывающие делают до охранника только чтение, поэтому квитанция освобождается и тот же вызов повторяется. Раньше фиксированный ключ воркера `integration_ship` оставался PENDING навсегда.
- **MCP-135** — `tool_handler`: истёкший ctx до запуска обработчика → `CONTEXT_DEADLINE` без эффекта, ключ свободен. PENDING-квитанция `fs_patch`/`fs_write` снимается повтором того же вызова, если файл всё ещё на `expected_sha256` (или отсутствует при создании) и в процессе нет другого живого вызова с этим ключом (`internal/mcpserver/reconcile.go`); внутренняя квитанция `runIdempotent` снимается вместе с ней. Прочие инструменты остаются fail-closed.

## Проверка и распространение

1. `project_test` с точными пакетами: `internal/rootbroker`, `internal/mcpserver`, `internal/executor`, `internal/projectlease`, `internal/protectedcore`, `internal/verify`.
2. Immutable GitVerse snapshot и проверка release-suite/contract CI для конкретного SHA.
3. `integration_ship evidence=gitverse` → `integration_approve` → защищённое продвижение в `main`.
4. `release_build_prepare` → `release_local` (при необходимости sealed stage) → `release_apply` по Face ID.
5. `release_apply_status`, `project_verify`, версия controller/broker, DNS/HTTPS/tunnel/rollback, `systemctl show mcpshka-broker.service`.

## Безопасность

В `main` не допускаются прямые коммиты, не затрагиваются локальные записи ISSUE_REGISTRY/ROADMAP. При отсутствии чистого checkout, Face ID, подтверждённого CI, или при UNKNOWN receipt — HOLD, без обхода. Rollback восстанавливает оба бинарника, root helpers и release-owned drop-in, не меняя пользовательские override-файлы.
