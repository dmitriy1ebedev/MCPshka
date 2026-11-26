# MCPshka 0.7.1 — 2026-11-04

Патч к 0.7.0: раздел «Проблемы» снова принимает изменения от ИИ.

## Исправлено

- **MCP-130 (P1).** У `issue_update`, `issue_accept` и `issue_dispatch` проба контракта `issue_get` вызывалась без id карточки. В итоге любая правка карточки останавливалась на «before probe failed; request held: issue not found». Теперь проба получает `id`; `issue_dispatch` тоже проверяет саму карточку (в ней — id задачи агента). Регрессия: `internal/toolcontract/issue_probe_test.go`.
- В коде, BUGLOG и CHANGELOG восстановление интеграций брокера (DIAG ACTIVE-01) теперь называется MCP-125, как карточка в «Проблемах». Номер MCP-118 занят карточкой r22 про песочницу kernel lab.

## Проверка

- `go test ./internal/toolcontract ./internal/mcpserver -run Issue|Contract`.
- После установки: `issue_update id=MCP-119 status=fixed commit=fc354fb…` проходит.
