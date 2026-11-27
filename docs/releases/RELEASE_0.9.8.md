# MCPshka 0.9.8

## Баги
- MCP-158 (Lane): `forge_ci provider=gitverse mode=rerun path=/root/repos/<проект> commit=<sha>` — повторный прогон CI проекта на том же коммите (workflow_dispatch, без force-push; main зеркала должен уже быть этим коммитом). `mode=status` помечает `infra:true`, если упала сама площадка (HTTP 502/503/504 при загрузке Python/Go, DNS), и сразу даёт `next` с rerun.
- Раннер не брал задания на GigaIDE: агент ставил sqlite3/PyYAML только через sudo, которого там нет. Агент v3 качает sqlite3 (sqlite.org) и PyYAML (`pip --target`) сам в папку раннера рядом с ключом; Go и инструменты больше не теряются при очистке `~`.
- `runner_task`/`runner_status` сразу говорят, почему задачу некому взять (`live runners lack sqlite3…`, `no runner seen`), а не через 10 мин NO_RUNNER.

## Балансировка (DIAG P34: «нет sealed-теста HEAD, H96 перегружен»)
- `phase_gate` при отсутствии теста HEAD читает CI проекта сам и называет одно следующее действие: CI идёт → ждать; сбой площадки → rerun; есть `.gitverse/workflows` → publish; живой раннер → `runner_task`; `project_test` на H96 — последним.
- Гайд `runners`: rerun, infra, «выполни next из phase_gate».

## Запасной CI и шардирование (владелец 09.11: «всё сам в 0.9.8»)
- `integration_ship evidence=auto`: GitVerse — основной; если упала сама площадка (ENV_FAILURE, недоступный API), MCPshka один раз перезапускает её и параллельно запускает GitHub CI (снимок + PR). Засчитывается первая площадка, прошедшая тесты (`gitverse` или `ci`). Падение кода на любой площадке — стоп без обхода. Логика вынесена в пакет `internal/shipci`.
- GitVerse `snapshot-contracts.yml`: полный набор разбит на 3 параллельных шарда (`release-shard-0` — `internal/mcpserver` + race, `-1` — половина остальных пакетов + native-контракты, `-2` — вторая половина + ARM64-сборка; скрипт `packaging/ci/release_shard.sh`). Шлюз по-прежнему проверяет задание `release-suite`: оно успешно только когда прошли все шарды.

## MCP-103 (Lane)
- `mutation_preflight effective=next_boot via_provider=lane`: изменение, которое действует только после перезагрузки (замена юнита), не имеет живого радиуса — пустые `affected_paths` законны. С проверенным резервом (Tailscale SSH) — GUARDED без ручного Face ID; без резерва — один понятный шаг «проверь независимый путь управления». Живые изменения с пустыми путями по-прежнему «blast radius unknown».
