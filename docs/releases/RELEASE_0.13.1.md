# MCPshka 0.13.1 — 2026-11-14

Ночной выпуск: Claude Code + Codex.

## Плагины (MCP-187)
- Страница `/connections/plugins`: добавить MCP-сервер по URL или из магазина, вход OAuth/токен, карточка со статусом, числом инструментов, статистикой и журналом вызовов (значения аргументов не пишутся).
- `plugin_tools` → `plugin_call` (или `tool_invoke <сервер>.<инструмент>`); пишущие/неизвестные инструменты — только с активной сессией Face ID. Секреты в аргументах → SECRET_BLOCKED.
- `plugin_sync` — сопоставление приложений ChatGPT/Claude с возможностями MCPshka.
- Инструменты `plugin_*` — resolve-only (каталог держится ≤ 250).

## Артефакты пайплайна (MCP-188, Codex)
- `pipeline:<путь>` в `approved_task_prepare`, `{artifact:<путь>}` в `project_release`, чтение экспортов через `artifact_get`.
- Общий даунгрейд `allow_downgrade` + `target_version` (`MCPSHKA_ALLOW_DOWNGRADE`), `webos_deploy` — тонкая обёртка.

## Исправлено
- MCP-190: ложное «staging failed» у `release_apply`. Срабатывает с брокера 0.13.1, то есть на выпуске после этого.
