# Хост и умный дом

- «Разберись» → `host_triage`: проблемы с доказательствами и готовым вызовом. Выполни действия по порядку, затем снова `host_triage`; доложи, что осталось сломанным.
- DIAG в триаже: при нездоровом хосте `host_triage` кладёт `diagnosis` (свежий отчёт DIAG) или готовый `diag_investigation_start` (problems kind=diagnosis, один прогон на 10 мин). Цикл: DIAG до → фикс → DIAG после → `diag_compare` (FIXED/IMPROVED/UNCHANGED/WORSE) — в отчёт владельцу вердикт сравнения.
- Новая DIAG на хост: `diag_release_apply` (коммит из main DIAG + версия из version.go, Face ID) → `diag_release_status` до INSTALLED/ROLLED_BACK. Не копировать бинарники руками.
- Новая MCPshka на хост: `release_apply` (после release_local) → `release_apply_status` до INSTALLED.
- Контейнеры: `docker_list`, `docker_status`, `docker_logs`, `docker_action` action=restart (только из разрешённого списка).
- Сервисы: `service_status`, журнал — `journal_read`, перезапуск — `service_action` action=restart.
- Home Assistant: `ha_states` (фильтр domain/query), `ha_state`, `ha_check_config` перед любым reload, `ha_service_call` (stop/restart самого HA запрещён — это `docker_action` restart homeassistant).
- H96 — прод: не запускай тяжёлое при `host_pressure` WARN, не трогай DNS/VPN/сеть без задачи.
- Проект в `/root/repos` (создать, перенести из `/root/<имя>`, отдать службе проект с файлами root): `project_repo_preview` → `project_repo_adopt` с его `plan_sha256`, под Face ID. Не chown руками и не скрипты владельцу.
- Сайт без API (кнопка GigaIDE, секреты GitVerse, настройки MosHub): `browser_run` (шаги goto/wait/click/type/text/screenshot на одном сайте) → `job_wait` → `job_output` → `browser_screenshot`. click/type — только с Face ID; пароли не вводить: владелец один раз передаёт cookies (`cookies` в `browser_run`), профиль сайта хранит сессию.
