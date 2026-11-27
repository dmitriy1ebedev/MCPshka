# MCPshka 0.11.5 — 20 пунктов бэклога ROADMAP

Выпуск закрывает 20 открытых пунктов `ROADMAP.md` (не очередь выпуска, а старый бэклог фаз). 17 закрыты новым кодом, 3 уже были в коде и приняты по существующим тестам. Новых инструментов нет: расширены имеющиеся, каталог не вырос.

## Закрыто кодом

| № | Пункт ROADMAP | Что сделано |
|---|---|---|
| 1 | P11: `fs_find` — статусы, курсор, пропуски | `status` completed / partial / max_results / timed_out / failed, `reason`, `cursor`, `skipped` (denied, large, binary), `denied_examples`; недоступная папка больше не отменяет найденное в остальных. Права и корни не ослаблены |
| 2 | P11: поиск с лимитом времени, regex, игнор-шаблонами; пакетное чтение по хешам | `fs_find regex`, `ignore`, `timeout_ms`; сессии на сервере нет — продолжение по `cursor`, остановка = не продолжать. `fs_read files=[{path, sha256}]` — до 16 файлов за вызов, неизменившийся возвращается без текста |
| 3 | P19, уровень 1: счётчики, доля успеха, частота отказов, эффективность восстановления | `tool_usage`: `success_rate`, `recovered`, `recovery_rate` по каждому инструменту и `overall` |
| 4 | P19: планировщик учитывает историю отказов, но не отключает инструмент | `tool_resolve`: `failure_history` у инструмента с 20+ вызовами и половиной отказов; при равном счёте такой идёт после надёжного; одна ошибка ничего не меняет |
| 5 | P23: источник и полнота данных статистики | `tool_usage.source`, `complete`; доля считается точно и не показывается без вызовов |
| 6 | S5.CM: `tool_resolve` отдаёт TS-декларации | `tool_resolve format=ts` — поле `ts` вместо JSON Schema (`fs_find`: 1888 байт против 2941) |
| 7 | S5.CM: замер до начала | тест бюджета обнаружения (tools/list против bootstrap + resolve) и `tool_usage.chains` — длина цепочек вызовов и доля вызовов через обходной маршрут |
| 8 | S5: ленивые схемы и пакетные чтения | закрыт пунктами 2 и 6 вместе с уже имевшимися `compact`, типизированными кодами отказов и `code_run` |
| 9 | P25: всё из браузера и коннекторов — UNTRUSTED | результат `browser_run` несёт `trust: UNTRUSTED` (облака и Home Assistant уже несли) |
| 10 | P27: скриншоты — UNTRUSTED | `browser_screenshot` отвечает с `trust: UNTRUSTED` |
| 11 | P24: аналитика прогресса ROADMAP и противоречий приёмки | `project_context.roadmap_progress`: сделано / всего, фазы DONE с открытыми пунктами, фазы без приёмки, расхождение таблицы статусов с разделами |
| 12 | P24: отчёт аудита инструментов | `tool_forge action=audit` → `report`: зарегистрировано, видно клиенту, скрыто, устаревшие дубли с заменой, контракты, состояния готовности, неиспользуемые мутации, недостающие возможности (открытые запросы) |
| 13 | P10: операции add / search / inspect / update / supersede / forget / export / import | `memory_recall id=` (просмотр), `memory_write forget=<id>` (уходит из выдачи, остаётся в истории), `erase=true` (удаление с индексом, вектором и осиротевшими вопросами) |
| 14 | P10: переносимый экспорт / импорт | `mcpshka memory-export FILE`, `mcpshka memory-import-file FILE [keep-canon]`: формат `mcpshka.memory/v1` (JSON lines, версия, SHA-256), секреты маскируются, id пересчитываются, повтор ничего не добавляет, отметка «канон» принимается только по явному слову |
| 15 | P11: индексы project / incident / artifact / user knowledge / device | `memory_recall kind=` и `tag=`: инциденты — `kind=problem`, знания о владельце — `kind=preference`, устройство и артефакт — тегом |
| 16 | P22: модель пользователя видна, правится, удаляется, переносится | вид записи `preference`; просмотр, правка (новая версия), `forget` / `erase`, экспорт |
| 17 | P22: модель пользователя не влияет на policy | архитектурный страж: пакеты policy, approval, trust, protectedcore, rootbroker, executor не импортируют память |

## Уже было в коде — принято по тестам

| № | Пункт ROADMAP | Доказательство |
|---|---|---|
| 18 | S1: классы отказов с детерминированным `next_safe_action` и запретом слепого повтора | `internal/failure` (`TestReasonClassAction`, `TestRecoveryDispositions`), `TestClassifyFailure*`, `TestToolCatalogLint` |
| 19 | P12: устаревший снимок инструментов клиента → `CLIENT_SCHEMA_STALE` и шаги обновления | `client_catalog.hint`, `client_schema_test.go` |
| 20 | P18: `CLIENT_SCHEMA_STALE` только при реальной несовместимости, с перечнем изменившегося | там же: `SchemaMismatch`, `changedCatalogSummary` |

Также: BUG-20261103-1 (неотслеживаемый файл ронял вливание без причины) — причина называется с 0.3.1 (`untrackedCollisions`, `CHECKOUT_NOT_ON_TARGET`), строка реестра закрыта.

## Модульность

Чтобы не раздувать `mcpserver`: TS-декларации — в `internal/codemode`, помощники создания папок `fs_write` — в `internal/filesystem`, сводка статистики — в `internal/toolstats`, отчёт аудита — в `internal/toolforge`. Поиск переписан одним проходом на Go вместо двух движков (ripgrep убран: он молчал о недоступных путях). `browser.go` уменьшен (пути — в `paths.go`), лимит храповика опущен 749 → 742.

## Не вошло

Пункты P5 «drop-in'ы с откатом и дрейф весов» и P8 «задержка публикации MQTT» требуют правок root-брокера и живых проб дома — остаются в бэклоге.

## Проверка после установки

`project_verify 0.11.5`; `fs_find` по `/root/repos` с `timeout_ms=200` (ответ `timed_out` с курсором); `tool_resolve goal=fs_find format=ts`; `tool_forge action=audit` (есть `report`); `project_context` (есть `roadmap_progress`); `mcpshka memory-export`.
