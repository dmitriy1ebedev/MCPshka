# MCPshka 0.1.178 — повтор прогона GitVerse, сверка брошенных permit

- BUG-20261023-78 (Codex): упавший на инфраструктуре прогон GitVerse повторяется для того же коммита — `forge_ci provider=gitverse mode=rerun commit=<sha>` (workflow_dispatch на ветке снимка), root-сверка берёт свежайший push/dispatch прогон тех же снимка и задач.
- Брошенные permit исполнителя (Codex): `executor_admin_stale_preview` / `executor_admin_stale_reconcile` под окном Face ID — permit без живого юнита и процессов, старше часа, переводятся в терминальное состояние по SHA-плану с аудитом; health исполнителя перестаёт быть вечным unknown.
- Первый выпуск, который MCPshka собирает и ставит сама: release_local (докачка) + release_apply.
