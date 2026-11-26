# Самодостройка MCPshka из чата

Нет нужного инструмента — MCPshka дописывает его сама, руками того ИИ, что сейчас в чате (через MCP-туннель).

1. Цепочка из существующих инструментов — `tool_forge action=compose` (x_-инструмент без выпуска, Face ID).
2. Нужен новый код — `tool_forge action=request` (goal, missing, acceptance), затем `tool_forge action=build request_id=<id> admin_session_id=<Face ID>`:
   - **по умолчанию** работу делаешь ты сам в чате: build выдаёт ветку `forge/<id>` в worktree `/root/repos/mcpshka-forge` и lease;
   - `agent=codex|claude` — только если владелец явно назвал исполнителя: build ставит задачу агенту (agent_task).
3. Цикл в чате (всё через инструменты MCPshka): `fs_find`/`fs_read` (найти, где живёт похожий инструмент: internal/<модуль> — логика, internal/mcpserver — регистрация) → `fs_patch`/`fs_write` в worktree → `project_test path=/root/repos/mcpshka-forge packages=[...]` → `project_commit` → `integration_ship evidence=gitverse` (сам запускает CI; опрашивать каждые ~60 с) → выпуск по `topic=release`.
4. Правила: лаконичный код, тест на каждое изменение, версию не поднимать без выпуска, Face ID — только владелец.
