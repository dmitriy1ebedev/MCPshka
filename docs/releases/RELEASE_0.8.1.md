# MCPshka 0.8.1 — безопасный автономный маршрут релиза

Дата подготовки: 2026-11-05.
Статус: CANDIDATE. Ни коммит, ни тест, ни CI, ни подготовленный план не означают production DONE.
База: защищённый main bf487c7c82adb48122e9f73daf7b669a08743c6b; установленная 0.8.0, rollback 0.7.3.

## Включённые исправления

- **MCP-085 / ROADMAP S1:** `tool_resolve` распознаёт RU/EN запросы на выпуск самой MCPshka и в первую очередь предлагает проверенный путь `release_prepare(source=github)` → `project_deploy` → `project_verify`; не подменяет выпуск MCPshka установкой DIAG или случайными health-tools. Возвращается точная цепочка с проверкой pinned CI и запретом replay UNKNOWN.
- **Lease recovery / S1:** запрос на снятие занятого lease выбирает `project_lease_admin_stale_preview` и `project_lease_admin_stale_release`; это рекомендации, не право обойти Face ID, живой процесс или 30-минутный remote-holder safety gate.
- **MCP-001 / S1:** `task_plan` раскладывает owner intent выпуска MCPshka в последовательность из 9 проверяемых операций: `project_context`, `integration_ship`, `integration_approve` (ручная Face ID проверка), повторный `integration_ship` с подписанным approval, `release_prepare`, `release_status`, `project_deploy`, `project_deploy_status`, `project_verify`. Выход планирования не даёт разрешения на мутацию.
- **ROADMAP S1 / resource safety:** mission preflight не выдаёт ложный `BLOCKED` из-за **только** необязательной resource-дееградации при исправных обязательных зависимостях; REQUIRED degraded, UNKNOWN или отсутствующие доказательства остаются HOLD. Механизм admission каждого реального тяжёлого задания и очередь не изменены.
- **Единые инструкции:** `mcpshka_guide(topic=release)` и `skills/mcpshka-release/SKILL.md` ставят signed paired CI-route основным, sealed build + fenced test permit + `release_apply` резервным.
- **MCP-143 / независимый CI:** `forge_ci` принимает собственный `workdir` непосредственного sibling-worktree с текущим `lease_token`, точным `HEAD` и тем же common Git store для **только** `plan/publish/status` immutable snapshot. `mirror`, `pr`, `rerun`, protected integration и deploy не наследуют эти права и требуют отдельного штатного owner gate. Решение устраняет ложный `OWNER_BUSY` при доступной независимой работе, но само по себе не выпускает версию в prod.
- **MCP-144 / брошенный root-lease:** `mcpshka.service` запускается из `/root/repos`, а не из защищённого репозитория: иначе `project_lease_admin_stale_preview` всегда видел сам прод как живого держателя. Ошибка `integration_ship` про `ROADMAP.md` теперь называет путь и просит передать корень репозитория.
- **Controller + broker:** согласованный version pin 0.8.1; бинарники должны быть собраны из одного protected commit и установлены только парой.

## Приёмка и ограничения

- Целевые RU/EN `tool_resolve`, `task_plan`, отрицательные тесты рекомендаций, test isolation. Отсутствующий инструмент или запрещённый эффект не заменяется диагностическим запросом.
- Полный sealed `project_test` и review на точном чистом HEAD, защищённая интеграция без force/rewrite, GitHub CI release/race/fuzz/build/release-arm64.
- Подготовка через `release_prepare` с pinned authenticated receipt, signed paired `project_deploy`, `project_deploy_status` и `project_verify(expected_version=0.8.1)`; реальные хеши running controller/broker, tunnel/DNS/HTTPS, rollback readiness и cleanup.
- S1 включает восемь сквозных требований ROADMAP. Реализация части маршрутизации **не** означает автоматически принятого S1; непроверенные fault-injection, resource, owner-preflight и autonomy acceptance остаются OPEN до независимых доказательств.
- RDC — только независимый break-glass при недоступности штатных MCPshka tools, не средство обхода lease, client policy или protected release gates.
