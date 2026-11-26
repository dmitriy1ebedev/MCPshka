# MCPshka 0.1.163 — любой репозиторий

## Новое
- **Подключение любого репозитория.** `project_onboard_preview` предлагает профиль (Make/Go/pytest/unittest/npm), `project_onboard` регистрирует его в MCPshka без коммита в сам репозиторий; профиль общий для всех worktree. `project_test` берёт тест из профиля.
- **Sealed-тест всего дерева.** Все отслеживаемые файлы коммита HEAD (исполняемые — 0555, ссылки/подмодули — HOLD), команда из профиля, офлайн, только чтение; бюджет как у теста модуля (scratch на диске, 200% CPU при Nice 19, 1600M, один тяжёлый набор за раз).
- **`phase_gate`** — один вердикт PASS/BLOCKED по фазе: чистый git, нет активных/UNKNOWN job, нагрузка на CPU (только предупреждение), sealed-тест на точный HEAD, гейты checkpoint (PASS/WAIVED).
- **`host_triage`** («разберись») — проблемы хоста с доказательствами и готовым вызовом исправления.
- **Home Assistant:** `ha_states`, `ha_state`, `ha_check_config`, `ha_service_call` (токен в приватном состоянии MCPshka; stop/restart HA запрещены).
- Ремонт fence — для любого репозитория в `/root/repos` (кроме защищённого хранилища).
- `mcpshka.service`: `ReadWritePaths=-/root/repos` (решают права файлов); `packaging/native/adopt-repo.sh` для репозиториев root.

## Исправлено
- BUG-20261021-24 (ложные BLOCKED в чате) → `phase_gate`; -25 (схема RawMessage); -26 (таймаут sealed-теста DIAG); -11 (параллельные тяжёлые сборки); -17 (unhealthy → host_triage).
- BUG-20261020-10 частично: тесты executor под root перезапускаются от `nobody` (21 → 13).
- CI: черновые PR не тратят минуты Actions, повторный push отменяет прогон (BUG-20261021-28).

## Документы
- `docs/BUGS.md` — как распознать, завести и закрыть баг; готовые диагнозы.
