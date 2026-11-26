# MCPshka 0.12.9 — агенты в своих worktree

## Зачем

В 0.12.7–0.12.8 `agent_task` в worktree в проде всё равно отказывал, а отмена задачи не отпускала её lease. Агент держал lease основного checkout и останавливал автовыпуск. Этот выпуск чинит оба места и даёт агенту собственный worktree.

## Агент в worktree

- Брокер работает с `ProtectHome=tmpfs` и не видит `/root/repos`, поэтому сам путь worktree проверить не мог. Теперь сервер сам находит репозиторий worktree (`agents.ScopeArgs`) и передаёт брокеру `WORKTREE REPO`. Согласие проверяется по репозиторию, а раннер ещё раз сверяет, что worktree относится именно к нему.
- `agent_task worktree:true` (P26): задача создаёт `/root/repos/wt/<repo>-agent-<id>` на ветке `agent/<id>` от HEAD и работает только там. Lease основного checkout задача не берёт.

## Отмена задачи

`agent_task_cancel` освобождал lease задачи серверным `ReleaseStale`. Ему нужен root-владелец SQLite, и он всегда отвечал «kept». Теперь отмена идёт тем же путём брокера, что `project_lease_admin_stale_preview/release`: брокер заново доказывает, что задача закончилась и у проекта нет заданий.

## Работа Codex (пункты ROADMAP)

Всё ниже видно в выходе существующих инструментов, тесты вызывают сами инструменты:

- P26 1277 — `agent_task_status`: `done_unverified` без коммитов и PASS теста.
- P13 — квоты и идентичность провайдера в `providers_status`/`agent_task_status`; AI-ревью не gate.
- P18 — «ждёт Face ID» по каждому challenge, ref/next_safe_action, состояние доставки; поля прогресса в `release_apply_status`/`mission_status`.
- P24 — выбор Go-пакетов по диффу; цепочка исходников в `project_context`.
- P19 — fingerprint и похожие инциденты в `issue_*`.
- P23 — тренды и находимость инструментов в `tool_usage.work`.
- P15 — ремонт производного индекса в тике (`self_heal_status`).
- P11, P12, P7 — индекс capabilities и delta в `tool_resolve`, handoff, матрица сбоев тестами.

Не сделано: настоящего push нет (Sender не настроен); P15 покрывает пока только индекс; 30-дневная история накопится в проде.
