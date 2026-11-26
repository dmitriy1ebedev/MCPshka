# MCPshka 0.10.2 — 2026-11-10

Почему ChatGPT ночью «спотыкался» на Lane и DIAG, и Google Drive.

- **Миссии больше не висят в BLOCKED/unknown (MCP-166).** Сканер задач агентов считал в бюджет 128 все файлы каталога, а у каждой задачи ещё `.prompt`/`.report.md`/`.runner.lock`. С октября источник `mission` был всегда `unknown`, `capability_preflight` давал `preflight`, а миссия Lane 1.3.0 стояла BLOCKED на L0–L5 с подсказкой «mission_status» (круг). Теперь считаются только `.json`, завершённые задачи с убранным worktree — история, а не «неполно».
- **Необязательный `providers=unknown` не блокирует шаг миссии**: это значит лишь «агенты не запускались». Обязательная зависимость в `unknown` по-прежнему блокирует.
- **knowledge_search**: если лучшая запись больше `max_bytes`, она возвращается с усечённым текстом (`…[truncated]`), а не ошибкой «budget exceeded».
- **Google Drive (MCP-165, код):** для drive включён штатный pacer rclone (`--low-level-retries=6 --tpslimit=4 --drive-pacer-min-sleep=200ms`) — `rateLimitExceeded` переждётся сам; явный отказ провайдера (rate limit/квота/авторизация) — FAILED, а не UNKNOWN, и не держит storage-воркер. Корень — общий client_id rclone: свой OAuth-клиент владельца зарегистрирован, но подключение не было завершено, и конфиг остался на общем ключе. Подключение теперь просит `drive.file` (только файлы MCPshka: проверка Google не нужна), подсказка велит опубликовать приложение (In production): в режиме Testing токен живёт 7 дней.
- **Правило 14:** полный `project_test` на H96 не запускать параллельно с CI того же коммита.
- Вне кода: USB-кулер H96 переехал из таймера в автоматизацию Home Assistant.
