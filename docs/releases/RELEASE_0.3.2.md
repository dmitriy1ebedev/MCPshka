# MCPshka 0.3.2 — ключи не сгорают на отказах, аренды-сироты видны, GitHub без root

Дата: 2026-11-03. База: main 60dbea7 (0.3.1). Один выпуск по решению владельца: все проблемы, найденные в боевом тесте Lane и при выпуске 0.3.1.

## Исправлено
- **Ключ идемпотентности (BUG-20261103-5).** Мутация, отказавшая без эффекта (`failure.Rejected`, поле `no_effect`), снимает свою PENDING-квитанцию (`idempotency.Store.Discard`, CAS по отпечатку). Так помечены ошибка ввода `forge_ci`, checkout-HOLD `integration_publish_main` и `LEASE_HEARTBEAT_STALE` (OWNER_BUSY, next — `project_lease_heartbeat`; на 0.3.1 так сгорел ключ project_test). `idempotency.Run` тоже освобождает ключ вместо записи Failed. Любой отказ допуска держателя при старте job (`jobs.holderRefusal`) — no-effect; `ValidateHolder` объясняет «нет аренды на <root>» (forge_ci сверяет токен с каноническим репо, токен forge-дерева давал «receipt is stale» и сжигал ключ). В самом `ValidateHolder` пометки нет: acquire зовёт его после эффекта. Любая другая ошибка по-прежнему держит ключ (UNKNOWN). TestNoEffectRefusalReleasesRetryKey, discard_test.
- **project_verify (BUG-20261103-6).** `git_clean` = нет отслеживаемых изменений и статус не обрезан; число неотслеживаемых файлов — в detail.
- **release_local (BUG-20261103-7).** COLLECTING: «N MiB collected» + `next_action` → `release_apply` (он сам перехеширует образы). Описание инструмента обновлено.
- **Аренды-сироты (BUG-20261103-9).** `project_lease_status`: держатель `workflow-<id>` без записи job или `roadmap-run:<id>` в состоянии stopped/done/interrupted, без jobs и claim → `writer_scope_state=orphaned`, `next_action=project_lease_admin_stale_preview_then_stale_release`. Сам выпуск по-прежнему только через Face ID stale path.
- **Нативные тесты (BUG-20261103-8)** и **воркфлоу MosHub-образа (BUG-20261103-3).**

## Добавлено
- **forge_ci provider=github (BUG-20261103-10)**: plan/publish/mirror/status через `packaging/forges/extra_forge.py` от mcpshka, HTTPS + `/var/lib/mcpshka/github/api_token`; status — Actions-прогоны с `head_sha` коммита (нужен успешный cloud.yml, любой провал — observed_fail).
- `CHANGELOG.md`, `BUGLOG.md`; README — раздел текущего выпуска.

## Проверка
- go test (H96, project_test): failure, idempotency, gitops, verify, releaseprep, forgeci, integrationqueue, mcpserver.
- python: packaging/forges, packaging/native (160 OK), packaging/ci.
- CI: GitVerse snapshot-contracts (полный шлюз), доли MosHub/GitHub по `shard.py`.

## Откат
`release_apply` хранит 0.3.1 как rollback-бинарь; схема данных не менялась (поле `no_effect` — omitempty).
