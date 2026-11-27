# MCPshka 0.16.0 — 2026-11-17

Любой ИИ работает с H96 только через MCPshka, без Desktop Commander, и быстрее.

- Паритет с Desktop Commander: `proc_start/send/read/list/kill` (живые процессы, stdin, PTY), `ps_list/ps_kill`, `fs_move`, `fs_mkdir`, `fs_read_many`, `http_get`, `search_start/more/stop`.
- PDF: `pdf_read` (pdftotext), `pdf_write` (кириллица, DejaVu, append через pdfunite).
- Скорость: каталог по умолчанию 40 инструментов вместо 313 (≈102 КиБ вместо 624 КиБ на ход модели), полный — `?tools=all` / `MCPSHKA_TOOL_CATALOG=all`, остальное через `tool_resolve` + `tool_invoke`; правило «больше двух чтений — один code_run»; в `tool_usage` HTTP- и серверное время по маршрутам openai-tunnel / funnel-claude / local.
- Журналы `lane-*`/`diag*` по префиксу (без транспортов); `rollback_then` в рецептах выпуска (нужен DIAG 0.7.1).

Код: Codex (agent_task ae289e8d/d3e92ab6, 374eddce/41c67323) + Claude Code; ревью и правка аудита proc_read — Claude Code.
