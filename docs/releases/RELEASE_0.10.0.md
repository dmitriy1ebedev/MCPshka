# MCPshka 0.10.0

Нумерация теперь просто 0.10, 0.11, …: каждая версия собирает всё накопленное — открытые карточки, хвосты веток и пункт ROADMAP.

## Code Mode — `code_run` (ROADMAP S5.CM, этап 1)
- Один вызов выполняет короткий JavaScript, который цепляет **read-only** инструменты MCPshka: `mcp.call(name, args)` → объект вывода, исключение при ошибке; `console.log` сохраняется; `return` — результат. Промежуточные ответы не идут через контекст модели (идея Cloudflare Code Mode; поиск/схемы у MCPshka уже были: `capabilities_bootstrap`, `tool_resolve`, `tool_invoke`).
- Движок goja (чистый Go, без Node и облака). Мутирующие инструменты, `tool_invoke` и сам `code_run` — `POLICY_DENIED`. Вызовы идут через тот же диспетчер, что `tool_invoke` (scope, аудит, receipts, `tool_usage` route=code).
- Лимиты: 1 запуск одновременно, 60 с (interrupt), 50 вызовов, 64 KiB результата, стек 256, сторож кучи +128 МиБ; удалены ArrayBuffer/typed arrays/Atomics (класс RCE goja 2026 и дешёвый путь к большим аллокациям).
- Этап 2 (мутации с idempotency_key и сверкой) — после приёмки на реальных цепочках.

## Карточки сами заводятся и сами закрываются
- Сторож сервера: раз в 15 мин host_triage; критическая проблема два прохода подряд → карточка «Сторож» (P1) с фактом и предложенным вызовом; одна на kind+subject, пока карточка активна.
- Коммит со строкой `Fixes MCP-NNN` (fix/fixed/closes/resolves) → при старте выпуска, чей коммит `<версия>:` на main его содержит, карточка становится fixed с этим коммитом и принимается `release:<версия>`; урок пишется в БЗ. `Refs MCP-NNN` только ссылается.
- `release:<v>` теперь принимает карточку любой уже работающей версии (≤ запущенной): накопленные fixed-карточки прошлых выпусков закрываются первым же стартом 0.10.0.

## MCP-159 — зависание H96 от нехватки памяти
- homeguard (брокер) раз в минуту читает MemAvailable, заполнение swap и скорость pswpin+pswpout и pgmajfault (PSI в ядре нет и плохо ловит zram): `memory_thrash` (обмен > 1000 стр/с или > 500 major faults/с при MemAvailable < 10 %) или `memory_low` (MemAvailable < 6 % и swap free < 15 %, как earlyoom) → дом degraded, тяжёлая очередь ждёт.
- host_triage: проблема `memory_thrash` с `host_diagnose symptom=oom`; WARN, если журнал живёт в /run при существующем /var/log/journal (relinquish-var).
- Ручное на хосте 09.11 (earlyoom, без /swapfile, журнал на диске) остаётся. Приёмка с искусственной нагрузкой — отдельно (карточка `Refs`).

## MCP-160
- Брокер пропускает `RequiredBy/BoundBy/ConsistsOf` — `service_action restart` с проверкой каскада снова работает (smbd); отказ называет свойство.
- `diag_investigation_start`: границы полей в схеме, ошибка называет нарушенное поле.

## Нагрузка H96: брокер без сотен sqlite3 в минуту
- Брокер (root) читал state.db одноразовым sqlite3 на каждый запрос (~700 запусков/мин): долгоживущий sqlite3 был запрещён для root. Теперь root держит его для базы с владельцем не-root (процесс и так работает от владельца — WAL-файлы его же), чтения идут через отдельный `-readonly` процесс. Базы root — по-прежнему одноразово.

## Google Drive: причина вместо «UNKNOWN»
- `storage_list/stat` при ошибке rclone отвечали `FENCED_UNKNOWN` без причины (stderr выбрасывался). Теперь — класс и последняя строка ошибки rclone (токеноподобное замаскировано): `AUTH_REQUIRED`, `NOT_FOUND`, `QUOTA_OR_RATE_LIMIT`, `NETWORK`; чтение больше не требует reconcile. Ежедневный зашифрованный бэкап состояния в Drive (свой путь) работает: квитанции verified 03–09.11.

## CI
- Шаг `add-apt-repository ppa:git-core` ограничен 120 с на попытку: Launchpad повесил шарды прогона 2054357 на 17+ минут.

## Прочее
- `project_context`/sealed gate читают ROADMAP с фазами `### Приоритет L0 — …` (Lane 1.3.0), если в нём нет ни одной `## P` фазы: ChatGPT видел это как «внутреннюю ошибку MCPshka».

## Откат
- `release_apply` предыдущей 0.9.9. Сторож и автоприёмка — только в `mcpshka mcp-http`; закрытую по ошибке карточку вернуть `issue_update status=open`.
