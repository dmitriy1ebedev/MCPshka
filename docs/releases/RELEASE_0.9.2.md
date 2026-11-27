# MCPshka 0.9.2 — дыры интеграции с проектами

Дата подготовки: 2026-11-08.
Статус: CANDIDATE до `project_verify expected_version=0.9.2`.
База: main 2c1f1d1 (0.9.1, откат — 0.9.1).

Выпуск закрывает косяки взаимодействия MCPshka с проектами (DIAG, NFSU2, Lane)
и остаток реестра после 0.9.1. После него владелец обновляет плагин ChatGPT и
тестирует через чат.

## Включено

- **MCP-140** — `diag_observer_status view=baseline|incidents` идёт через брокер:
  root запускает только `/usr/local/bin/diag` (root-owned, без записи group/other)
  с фиксированным argv `baseline status|incidents --json --status-path … --window`,
  окно `^[1-9][0-9]{0,2}[mh]$`, 15 с, ≤ 64 КиБ JSON. Локального exec больше нет.
- **MCP-116** — `endpoint_probe via=tailnet` берёт имена tailnet из
  `tailscale status --json` (Self+Peer DNSName → IPv4), а не через DNS, который
  локальный резолвер перехватывал; `total_ms` больше не 0.
- **MCP-146** — sealed-тест проекта видит собственные неотслеживаемые каталоги
  (`/root/repos/<проект>/private/...`, кроме `.git`) без ручного
  `/etc/mcpshka/runtime-paths.allow`; симлинки за пределы по-прежнему закрыты.
  Отказы называют причину (`RUNTIME_PATH_NOT_ALLOWED|ESCAPES|NOT_DIR|RESERVED`,
  `TREE_TEST_SPEC_INVALID`), а stderr подготовки рецепта попадает в журнал
  (раньше наследовал `StandardOutput=null` — exit 1 без текста).
- **oob-tunnel** — при неудачах пауза между рестартами растёт до 15 мин
  (`RestartSteps=6`, `RestartMaxDelaySec=15min`); сменённый ключ VPS давал
  ~5 760 запусков ssh в сутки. Шаблон юнита; живой юнит — через `install-oob.sh`.

## Приёмка

- `project_verify expected_version=0.9.2`.
- MCP-140: `diag_observer_status view=baseline window=1h` отдаёт JSON DIAG.
- MCP-116: `endpoint_probe url=https://<host>.ts.net via=tailnet` — стадии dns/connect/tls/http.
- MCP-146: NFSU2 `project_test` с `runtime_paths=private/engine-test…` проходит подготовку.
