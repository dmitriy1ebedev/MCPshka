# MCPshka 0.4.0 — «Подключения»: токены, облака и вход агентов с телефона

Дата: 2026-11-03. База: main 76e5ac4 (0.3.2 + CI MosHub-образа). Один полноценный выпуск по решению владельца: вся страница «Подключения» и проблемы из аудита ChatGPT 03.11.

## Добавлено
- **«Подключения» (`/connections` на `MCPSHKA_PASSKEY_ORIGIN`).** Обработчик в `passkeyOnlyHandler` за `portalGuard` (Host, Origin на POST, no-store, CSP с nonce для единственного скрипта; данные только через `textContent`).
  - Вход: `POST /connections/login` — новый грант root broker (`passkey connections-begin <id> <sha256(secret)>`, scope `connections_portal`, owner `connections-portal`), секрет — только в HttpOnly/Secure/SameSite=Strict cookie на `/connections` (5 мин). Браузер подтверждает Face ID через обычный `/approval/<id>/finish`. `POST /connections/session` — `connections-consume` (однократно, привязка к секрету), сессия в памяти контроллера, cookie 30 мин. Не больше 8 незавершённых входов.
  - Токены (`/connections/api/token`, `check`): длина 8–4096, без пробелов и управляющих символов; проверка у провайдера (GitHub `/user` + срок и права из заголовков, GitVerse `/user`, MosHub `/user` + `personal_access_tokens/self`, SourceCraft — репозиторий, GitFlic `/user/me`, Home Assistant `/api/`, Telegram `getMe`); только при успехе — атомарная замена файла 0400 в `/var/lib/mcpshka/...` (те же пути, что читают forge_ci и ha_*; Telegram — новый `secrets/telegram.bot_token`). Ответы — аккаунт, права, срок, `…last4`.
  - Облака: `cloud-app` = `storage_oauth_app_set`, `cloud-connect` = существующий `storageWeb.begin` (PKCE, callback `/storage/callback` на портале); состояние — `storage providers` (`credential_present`).
  - Codex: root broker `agent-login codex-begin|codex-status`. Фиксированный юнит `mcpshka-codex-login` (`codex login --device-auth`, HOME=/root, 15 мин, 256M, 50% CPU); из журнала юнита берутся только ссылка `https://auth.openai.com/…` и код `XXXX-XXXXX`. Статус — первая строка `codex login status` («Logged in…»/«Not logged in»), иначе «status unavailable».
- **Самоочистка executor (BUG-20261103-12).** `startExecutorSelfHeal`: через 3 мин после старта и затем каждые 15 мин — broker `executor-self-heal self-heal-stale-permits` (не чаще раза в 10 мин) → тот же изолированный `lease-recovery-worker`, `PreviewStalePermitsAuthority` и `ReconcileStalePermitsAuthority` по точному плану preview, авторизация `self-heal:proven-abandoned-permits:v1` в аудит-квитанции. Ничего не убивает и не останавливает; пропущенные (недоказуемые) остаются для Face ID-пути. В аудит контроллера пишется только «fenced N» или ошибка.
- `owner_portal` даёт ссылку на «Подключения».

## Исправлено
- **knowledge_write (BUG-20261103-11):** `knowledge.ErrInvalid`/`ErrLimit`/неизвестное действие → `failure.Rejected` (no-effect), ключ остаётся для исправленного вызова.
- **host_triage (BUG-20261103-13):** `serviceDown` — masked/masked-runtime и not-found не авария (как inactive+disabled).
- **tool_forge audit (BUG-20261103-14):** `delegatingTools` — `integration_ship`, `tool_invoke`; тест держит список узким.
- MosHub CI-образ без RUN-шагов (crane), `status_github` по последнему прогону — из [Unreleased] после 0.3.2.

## Известное
- BUG-20261103-15: секрет GitVerse `MOSHUB_REGISTRY_TOKEN` устарел (push → 401); после обновления владельцем образ собирается dispatch'ем `moshub-ci-image.yml`.
- Claude Code видит устаревшую схему каталога до переподключения клиента — это кэш клиента, не сервер.

## Проверка
- go test: mcpserver (Connections*, DelegatingTools, ExecutorSelfHeal*), rootbroker (ParseDeviceLogin, AgentLogin*, ExecutorSelfHeal*), hosttriage, knowledgeops; `go build ./...`, `go vet` затронутых пакетов.
- CI: GitVerse snapshot-contracts (полный шлюз), GitHub race-доля.

## Откат
`release_apply` хранит 0.3.2. Состояние не мигрирует: сессии «Подключений» в памяти, гранты — обычные файлы broker, токены — те же файлы.
