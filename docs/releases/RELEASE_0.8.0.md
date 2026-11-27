# MCPshka 0.8.0 — единый выпуск взаимодействия и надёжности

Дата подготовки: 2026-11-04.
Статус: CANDIDATE, НЕ УСТАНОВЛЕН (пока нет подтверждённых CI, интеграции, установки и production verify).
Ветка: `release/mcpshka-tier12-unified-20261104`. Базовая установленная версия: 0.7.3, резервная: 0.7.2.

## Состав единого выпуска

- **MCP-113 / Tier 1 P7:** тяжёлая FIFO-очередь показывает `head_label`, `ahead_count`, `head_running_seconds`, `queued_at`, `wait_seconds`. Значения пересчитываются по живым ticket, не накапливаются при status polling; нет обещания ETA без проверенной статистики.
- **MCP-046 (часть) / P8:** `host_triage` больше не сообщает `healthy` при усечённом списке jobs или невидимых в bounded page UNKNOWN. Для полного закрытия MCP-046 нужна отдельная E2E-регрессия broker/executor degraded при пустом реестре jobs.
- **S1 / P7 / Tier 2:** `mission_report` раздельно возвращает `implementation_complete`, `integrated`, `production_verified`, `accepted`. Каждое поле `UNKNOWN`, пока нет соответствующего подтверждённого gate на текущем HEAD; `DONE` не подменяет production delivery.
- **Repo adoption / recovery:** исправлена передача исходных `project_repo_preview` arguments при before/after observation; для потерянной квитанции запись остаётся UNKNOWN, без replay.
- **Единый реестр:** сохранён актуальный r22 с проблемами Lane; диагностические требования DIAG; решения DISRUPT; старые версии больших issue-registry перенесены в `docs/archive/`. Актуальные статусы проблем определяются `issue_list`, а не историческими файлами.
- **Controller+broker:** согласованный version pin 0.8.0, сборка и guarded deploy только парой.

## Не включено как готовое

- Независимые OOB пути, испытания с настоящим падением туннеля и полноценные P4/P7/P8 acceptance остаются отдельными неисполненными gates.
- Ветка `codex/perf-search-byte-budget-20261027`: старый поиск не включён, поскольку в main уже реализован более эффективный byte counter.
- Старые backup workflow/checkpoint ветки: функциональность уже обнаружена в main; дублирование не выполнялось.
- DIAG/LANE продолжают собственный lifecycle; установка MCPshka не подтверждает их release acceptance.

## Гейты единственного релиза

1. Целевые регрессии `heavyqueue`, `hosttriage`, `mission`, `toolcontract`, затем полный sealed `project_test` на чистом HEAD.
2. Независимая GitVerse (или разрешённая GitHub) CI по точному immutable source commit.
3. `integration_ship` с владельческим Face ID review в защищённый `main`, без forced merge и обхода других writer.
4. `release_build_prepare`: парные ARM64 controller/broker, pinned digest и fenced test permit.
5. `release_apply`, затем reconnect, `release_apply_status`, `project_verify(expected_version=0.8.0)`; проверка бинарных SHA, health, DNS, tunnel и сохранившегося rollback.
6. Проверка очереди и live-функций на production, обновление карточек только через подтверждённые evidence, cleanup и acceptance. До выполнения всех гейтов версия не считается установленной.
