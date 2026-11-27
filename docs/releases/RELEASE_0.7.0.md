# MCPshka 0.7.0 — «Проблемы», честная приёмка фаз, устойчивое восстановление

Дата: 2026-11-04. База: main c401002 (0.6.0) + feat/lanelink bcf34f0.
Источник дефектов: отчёт DIAG Tier 2 `docs/DIAG_TIER2_MCPshka_DEFECTS_2026-11-04.md` и реестр r20 (MCP-112…117).

## Добавлено
- **«Проблемы»** (`internal/issues`, страница `/connections/issues` под Face ID-входом «Подключений»). Карточки MCP-NNN с приоритетом P0–P3, статусом open → in_progress → fixed (нужен коммит) → closed (только приёмкой), rejected (нужна причина), фильтры, поиск, похожие карточки, история. Кнопка «В работу» запускает `agent_task` с карточкой, похожими карточками и опытом из базы знаний в задании, в открытом Face ID-окне владельца; без окна карточка помечается «ждёт запуска».
- Инструменты `issue_list`, `issue_get`, `issue_create` (сначала показывает похожие и опыт из БЗ), `issue_update`, `issue_accept` (доказательство `job:<id>` проверяется MCPshka, `release:<версия>` — только работающая), `issue_import` (из markdown-реестра, существующие id не трогает), `issue_dispatch`.
- Приёмка карточки пишет урок `lesson-mcp-NNN` в базу знаний; правила для ИИ (`topic=rules` 14–15): сначала `knowledge_search` и `issue_list`, проблемы — только через `issue_*`.
- `phase_checkpoint_record`: критерии приёмки фазы с доказательствами (`job:<id>` этого проекта, `min_duration_seconds` для soak), привязка к HEAD, срок годности, без самоотмены (waiver — решение владельца).
- OAuth issuer по хосту запроса: адрес порта Lane (с путём) и `MCPSHKA_OAUTH_ALIASES` получают свой issuer; метаданные и по RFC 8414/9728 со вставкой пути. Без этого ChatGPT/Claude через Lane не авторизовались.
- «MCP-порт через Lane» (`internal/lanelink`, из feat/lanelink).
- «Яндекс Диск по паролю приложения» на «Подключениях» (WebDAV, без регистрации OAuth-приложения).

## Исправлено
- **MCP-125 (DIAG ACTIVE-01, P0).** Восстановление интеграций в брокере каждые 30 с заново доказывало все 60 исторических запусков (retire + десятки sqlite3) и под нагрузкой всегда упиралось в 10 с. Закрытые запуски запоминаются на жизнь процесса, у каждого свой бюджет 20 с, проход 75 с; три неудачных прохода подряд → health executor `degraded` (`recovery.failed_sweeps`).
- **MCP-119 (ACTIVE-02, P0).** `phase_gate` с фазой без чекпоинта — BLOCKED (`checkpoint: MISSING`), поле `acceptance`: CODE_TEST_PASS / PHASE_ACCEPTED / INCOMPLETE. PASS-критерий без доказательства, с проваленным/чужим/коротким заданием, просроченный или с HEAD вне истории — FAIL.
- **MCP-120 (ACTIVE-05).** Юнит брокера: `CPUQuota` 50% → 100%, `CPUWeight=400`, `IOWeight=400` (тесты: CPUWeight 20, Nice 19, idle IO). Живой юнит выпуском не обновляется — ставится отдельно (см. «Установка»).
- **MCP-121 (ACTIVE-04).** Запись в проект с активным заданием — `OWNER_BUSY/PROJECT_BUSY` с id задания и `next_safe_action=job_wait`, «nothing was written»; не `FENCED_UNKNOWN`.
- **MCP-122 (ACTIVE-06).** CPU-бюджет по умолчанию масштабируется от стандартного wall (30 мин), а не от запрошенного: `timeout_seconds=7200` даёт 7200 CPU-с, а не 1800. Явный CPU-лимит не раздувается. Потолок 6 ч.
- **MCP-123 (ACTIVE-08).** Журналы служб health-поверхности (`diagd.service`) и своих транзиентных юнитов читаются `service_logs`; вывод маскирует bearer/token/password/URL-учётки. Сетевые юниты и произвольные журналы по-прежнему закрыты.
- **MCP-124 (ACTIVE-03).** `job_status`/`job_wait` идущего sealed-теста показывают живой cgroup исполнителя: CPU, RSS, I/O, процессы — тишина вывода больше не выглядит зависанием.
- **MCP-112.** Повторный finish/перезагрузка страницы Face ID после подтверждения отвечают «authorized» («Уже подтверждено»), а не «no such file or directory».
- **MCP-117.** `job_wait` с `timeout_seconds` > 120 не ошибка: ожидание обрезается до 45 с.

## Не вошло (остаётся открытым в «Проблемах»)
- ACTIVE-07 `CLIENT_SCHEMA_STALE` — снимок клиента; маршрут `tool_resolve → tool_invoke` работает.
- Мастер регистрации Google/Dropbox-приложения и iCloud Drive через rclone.
- HISTORICAL-09/10 — проверяются полным module-тестом выпуска.

## Установка
`release_apply` (пара controller/broker). Юнит брокера (MCP-120) выпуск не перезаписывает, а `install-broker.sh` переписал бы `broker.json`. Поэтому на хосте — один approved_task (Face ID): `systemctl set-property mcpshka-broker.service CPUQuota=100% CPUWeight=400 IOWeight=400` (drop-in, без перезапуска; откат — тот же `set-property` с прежними значениями).

## Откат
`release_apply` на 0.6.0 (предыдущий выпуск сохраняется установщиком).
