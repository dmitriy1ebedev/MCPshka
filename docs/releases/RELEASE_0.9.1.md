# MCPshka 0.9.1 — закрытие issue

Дата подготовки: 2026-11-08.
Статус: CANDIDATE до `project_verify expected_version=0.9.1`.
База: main 9551a21 (0.9.0, откат — 0.9.0).

Выпуск закрывает карточки реестра issue, найденные при сверке после 0.9.0.

## Включено

- **MCP-137** — `integration_ship`: before-проба не получает `action` и
  `idempotency_key` мутации; обход `action=""` больше не нужен.
- **MCP-011** — лимит чтения ROADMAP 512 КиБ (файл 257 КиБ упирался в 256).
- **MCP-046** — `host_triage` при здоровом хосте показывает готовность исполнения
  (`execution.state`, не-ok источники) и ведёт к `health_snapshot`.
- **MCP-034** — отказ интерпретатору/inline-коду в `exec_*` называет маршрут
  `project_run`/`project_build`/`project_test`.
- **MCP-114/115** — `approved_task` проверяет до 12 юнитов; во время RUNNING
  отдаёт хвост живого вывода (live.log ≤ 64 КиБ).
- **MCP-142** — release-gate снимает свои защитные rlimits перед `execve` helper.
- **MCP-116** — новый `endpoint_probe(url, via=tailnet|public|lan)`: резолв через
  MagicDNS/публичный DNS/локальный, отдельно DNS, TLS и HTTP-код. Только свои
  адреса (*.ts.net, частные, tailnet, loopback).
- **MCP-105** — грязное дерево у `project_test` — типизированный отказ
  `WORKTREE_DIRTY` без эффекта (было FENCED_UNKNOWN). Python-тесты Lane идут
  через очередь (UNIT_TESTS_OK, job mw7hh6zs-9).
- **MCP-118** — песочница тестов: `/usr/sbin` в PATH и `/etc/alternatives`
  только для чтения — iptables/ipset/ip доступны netns-лабораториям проектов;
  сеть песочницы по-прежнему изолирована, capabilities сброшены.
- **MCP-133** — ключ хоста LG TV закреплён за ТВ (`HostKeyAlias=lg-tv`), смена
  DHCP-адреса больше не ломает `webos_test`.

- **MCP-140** — `diag_observer_status(view=status|baseline|incidents)`: статус
  наблюдателя DIAG читается через брокер (один фиксированный root-файл, ≤ 64 КиБ,
  JSON, без симлинков) — права /run/diag 0700 не ослаблены.

## Приёмка

- `project_verify expected_version=0.9.1`.
- MCP-137: `integration_ship` проходит fence без ручного вызова.
- MCP-118: повтор `project_test` Lane — NATIVE_*_NETNS без SKIP.
- MCP-116: `endpoint_probe` к Lane Web по tailnet.
- MCP-140: `diag_observer_status view=baseline` возвращает состояние, не observer_status_unavailable.
