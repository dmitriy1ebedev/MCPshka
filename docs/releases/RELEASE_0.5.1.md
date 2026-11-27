# MCPshka 0.5.1 — гибкие таймауты на всём пути и доказательства для больших репозиториев

Дата: 2026-11-03. База: main 00ebd76 (0.5.0) + 7ffe322 (правка ChatGPT в проходе DIAG Tier 2).

## Исправлено
- **BUG-20261103-24.** 0.5.0 подняла бюджет DIAG P25 до 1944 с по истории, но `executor.PrepareTreeTest`, `PrepareRecipeTest` и `AbandonRecipePreparation` ещё сверялись с 1800: подготовка отвергалась, задание mw0sqrk1-5 оставалось UNKNOWN, а отмена отвергала тот же бюджет. Коммит 7ffe322 (ChatGPT): `validRecipeTimeout` на `budget.CeilingSeconds`, регрессия на 1944 с.
- **Системная защита.** `internal/budget/ceiling_guard_test.go` — `TestNoNumericTimeoutCapsInJobChain`: любой `timeout/ttl… > N` числом в пакетах цепочки заданий — ошибка теста; исключение одно (`rootbroker/approved_task.go`, свой контракт 10..1800 с).
- **BUG-20261103-25.** `projectlease.retirementGit` держал вывод в 8 КиБ — проверка подмодулей (`ls-files --format=%(objectmode)`, 7 байт на файл) ломалась на ~1170 файлах. Теперь все Git-чтения восстановления и отставки аренды под одним бюджетом `recoveryGitOutputBudget` (2 МиБ); переполнение называет бюджет. Тест на репозитории из 3000 файлов.

- **BUG-20261103-26.** `workbenchruntime.abandoned`: отказ `abandon-recipe` добавляется к ошибке наблюдения («preparation never sealed a permit and abandon refused: …»).
- **BUG-20261103-27.** `systemdsvc.OwnTransientUnit` (`^mcpshka-(executor|integration-admin|integration-worker|release-apply|lease-recovery|storage|recipe|workbench|stale-permit|self-heal|agent-task)-…\.service$`) — в контроллере (`requireStatusAllowed`) и в root broker (`validateJournalctl`, `statusReadable`). Только чтение; `mcpshka-codex-login` не входит.
- **BUG-20261103-28.** `workflowCoordinator.HolderGuard`: на LEASE_HEARTBEAT_STALE — `HeartbeatRefresh` тем же токеном и повтор проверки. `jobs.SetHolderKeepalive`: задание с `HolderToken` (local и sealed) продлевает аренду каждые `HolderKeepaliveInterval` (2 мин), пока не завершится, не станет UNKNOWN или не выйдет за `budget.Ceiling`.

- **«Подключения» (BUG-20261103-29/30).** `landedQuery`: code/state/scope из любого текста (без схемы, только query, percent-encoded); значения уходят лишь ожидающему rclone, который сверяет state. Карточка Codex — по `agent-login codex-status` (connected/missing). CSS: `form.stack`, на ширине ≤ 560 px поля и кнопки во всю ширину, кнопки 40 px.

- **BUG-20261103-31.** Root broker `passkey resolve <8..31 hex | latest>` → ID одного неотменённого подтверждения младше 1 ч (неоднозначный префикс — отказ); портал `GET /approval/resolve/<prefix>`; страница берёт ведущие hex-символы пути, при неполном ID спрашивает resolve и подменяет адрес. `admin_session_begin` отдаёт ссылку из 8 символов. Face ID по-прежнему обязателен для завершения.

- **BUG-20261103-32.** `cievidence.FetchGitVerse`: листание останавливается, когда подходящий прогон найден и все прочитанные ID строго убывают; неупорядоченный полный список по-прежнему HOLD. Сама 0.5.1 влита с evidence=ci (GitHub), потому что проверку выполняет установленная 0.5.0.

- **BUG-20261103-33.** `invariants.TestEveryInvariantNamesExistingTests` пропускается, если в дереве нет docs/ (снимок sealed module test); с docs/ и без INVARIANTS.md по-прежнему падает.

## После установки
- DIAG: сверить задание mw0sqrk1-5 (отмена принимает 1944 с), повторить полную матрицу P25.

## Проверка
- go test: budget (NoNumericTimeoutCaps), projectlease (Recovery*), executor (RecipeTimeoutBoundaries с 1944), rootbroker; CI GitVerse.
