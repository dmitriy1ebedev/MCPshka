# MCPshka 0.5.0 — «Подключения» v2: облака одной кнопкой, все «скоро» работают

Дата: 2026-11-03. База: main 408abd8 (0.4.0). Один выпуск по решению владельца: отзыв о «Подключениях», повторная проверка ChatGPT 03.11 и сбои MosHub CI.

## Добавлено
- **Облака без своих OAuth-приложений (BUG-20261103-21).** `connections_cloud.go`: `cloud-start` запускает дочерний `rclone authorize <backend> --auth-no-open-browser` (временный HOME/конфиг, один вход за раз — порт 53682 фиксирован, 10 мин), берёт из stderr `http://127.0.0.1:53682/auth?state=…`, сам открывает его без редиректа и отдаёт странице `Location` — адрес входа провайдера. `cloud-finish` принимает адрес, на котором остановился браузер (только хост 127.0.0.1/localhost:53682, нужны `code` и `state`), передаёт `state`/`code`/`scope` ожидающему rclone, читает токен между `--->` и `<---End paste` (JSON или base64-конфиг) и отдаёт его root-хранилищу (`auth_accept`, owner `connections-portal`). Состояние: `credential_present` → «подключено», иначе «не подключено». Своё приложение (client id/secret, redirect URI) — в «Дополнительно».
- **OneDrive.** `storagefabric`: провайдер `onedrive`, `Accept` требует `drive_id`/`drive_type` (`^[A-Za-z0-9!._-]{1,128}$`) и пишет их строками конфига rclone; контроллер получает их из Graph `/me/drive` после входа.
- **Формы (`connections_forms.go`).** Тип `form`, хранение — один JSON 0400 `secrets/<id>.json` после успешной проверки; поля проверяются по шаблонам, лишние отвергаются, пустой секрет оставляет сохранённый, каталог отдаёт секреты только как `…last4`.
  - ntfy (GET `<server>/<topic>/json?poll=1`), Tailscale (`/api/v2/tailnet/<tailnet>/devices`), Cloudflare (`/user/tokens/verify`, status active), WebDAV/Nextcloud и Mail.ru (`PROPFIND Depth: 0` → 207, `webdav.cloud.mail.ru`), S3 (ListObjectsV2 max-keys=1, SigV4; по умолчанию `storage.yandexcloud.net`, `ru-central1`), restic (сохранение; проверка — при первом бэкапе).
  - Claude Code: токен `secrets/claude.oauth_token`, проверка формата `sk-ant-oat01-…`; использование агентами — следующим шагом.
- **MosHub → GitVerse.** `ci-secret`: по нажатию в карточке MosHub сохранённый токен уходит PUT в `actions/secrets/MOSHUB_REGISTRY_TOKEN` с сохранённым токеном GitVerse.
- `packaging/tunnel/mcpshka-faceid.caddy` — текущий конфиг публичного входа (пути `/connections*`, `/storage/connect/*`, `/storage/callback`).

- **Гибкие таймауты (BUG-20261103-22, DIAG P25).** Пакет `internal/budget` — одно правило для всех путей: `Decide` (запрос → не ниже p95×2+2 мин истории того же command/args/workdir; иначе история или умолчание; множитель нагрузки load1/CPU, 1..2; потолок `Ceiling` 6 ч), `Extension` (половина бюджета, ≥ 10 мин, до потолка; не для бюджетов < 20 мин), окно тишины треть бюджета в пределах 20–60 мин. `jobs.Manager`: `Budget()`, история успешных прогонов, `watchTimeout` продлевает при активности за последние 5 мин (CPU-тики, запись на диск, вывод) и пишет `job_extended` в аудит, `watchResources` снимает «stalled». Лимит 1800 с заменён на `budget.CeilingSeconds` в `executor.RunSpec`, `workbenchruntime.decode`, `rootbroker.recipePrepareTimeout` и проверке spec, `pipeline`; `projectjobs.ProfileOptions` — таймаут профиля как умолчание. Новых полей в выходных схемах нет (старые клиенты не ломаются): объяснение — в `plan.reason` и аудите.
- **CI сама (BUG-20261103-23).** `integration_ship`: при `evidence=gitverse` шаг `shipCI` перед запечатыванием — `forge_ci gitverse status` (job_wait + job_output); нет прогонов → publish на gitverse + moshub + github; идёт → CI_RUNNING; падение → CI_FAILED с первой строкой выдержки и next_action «новый коммит»; observed_pass → обычный путь. После PUBLISHED — mirror на gitverse, github, sourcecraft. Воркфлоу `moshub-ci-image.yml` после push ставит `MOSHUB_CI_IMAGE` через API MosHub.

## Изменено
- **Кто разрабатывает (BUG-20261103-20).** `agentguide.DevelopmentInstructions`, `guide/rules.md` (правило 12), `guide/roadmap.md`, `guide/agents.md`: разработку ведёт ИИ в чате инструментами MCPshka; `agent_task`/`roadmap_run` — только по прямому указанию человека, их отсутствие не блокер.
- **roadmap_run_status (BUG-20261103-19):** `superseded_by` и `next_action: none: superseded by <id>` у прогонов, после которых был более новый на том же пути (кроме `reconciliation_required`).

## Исправлено
- **Вход Codex (BUG-20261103-16):** юнит стартует `/usr/bin/env HOME=/root NO_COLOR=1 /root/.local/bin/codex login --device-auth`: путь ищет уже процесс в юните, а не systemd-run в пространстве имён брокера (ProtectHome).
- **Ложное DEGRADED resources (BUG-20261103-18):** при окне замеров < 2 ч предупреждение только если до резерва ≤ 12 ч (иначе 72 ч).
- **MosHub CI-образ (BUG-20261103-17):** база `golang:1.26.0-trixie` (git 2.47: `--no-lazy-fetch`), `python3-yaml`; `assemble.sh` проверяет, что digest — `sha256:` + 64 hex, воркфлоу — что артефакт заканчивается digest'ом.

## После установки
- Образ MosHub пересоберётся сам при вливании (меняются его файлы) и сам пропишет `MOSHUB_CI_IMAGE`; затем перезапустить CI MosHub.
- DIAG P25: повторить полную матрицу — бюджет поднимется по истории и нагрузке, зависание снимет сторож.
- Владельцу: войти в Codex заново на «Подключениях».

## Проверка
- go test: mcpserver (Connections*, CloudFlow, RcloneToken, OneDriveDrive, ConnectionForms, S3RequestIsSigned, MosHubSecretPush), storagefabric (Vault*), rootbroker (CodexLoginStartsThroughEnv), systeminfo (storage), roadmaprun (superseded), agentguide; `go vet` затронутых пакетов; CI GitVerse.
