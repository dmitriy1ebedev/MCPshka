# MCPshka 0.1.183 — SourceCraft и GitFlic в forge_ci, ship сам закрывает воркер

- C2 (шаг 1): `forge_ci` работает с четырьмя площадками — GitVerse, MosHub, SourceCraft, GitFlic (publish/status; mirror — GitVerse и SourceCraft). Снимок SourceCraft/GitFlic — сам исходный коммит в `mcpshka-snapshots/<sha>`, неизменяемый. SourceCraft-прогон привязывается к коммиту через артефакт report.json (у прогонов SourceCraft нет коммита в API). Конфиг GitFlic исправлен (CONFIG_ERROR из-за CI_COMMIT_BRANCH). Main SourceCraft был пуст — триггеры не срабатывали, теперь зеркалится.
- BUG-20261024-95 (вторая попытка): ship читал результат воркера из идемпотентного ответа integration_worker_launch, где его нет; теперь читает живой integration_worker_status и сам вызывает fence.
