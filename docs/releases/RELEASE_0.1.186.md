# MCPshka 0.1.186

Включает 0.1.185 (подключение облаков из браузера) и 0.1.184.

## Docker packaging smoke без прав docker (DIAG P12)

- `docker_smoke` — сборка образа проекта из закоммиченного дерева `path@commit` (git читает репозиторий от его владельца, контекст — `git archive`) и запуск контейнеров в фиксированных профилях: uid 65532, read-only, без capabilities, no-new-privileges, 96 MiB / 64 pid / 0.1 CPU, 90 с на запуск.
- Профили: `isolated` (без сети) и `host-observer` (host pid/net/cgroup, `/sys` и `/etc/os-release` только чтение в `/host/...`). Общий том на запрос, entrypoint `/bin/sh` или `/usr/local/bin/<имя>`.
- Ожидает тяжёлую очередь; образ, том и контейнеры удаляются после прогона. `docker_smoke_status` — коды выхода и stdout (8 KiB) каждого запуска; проверки делает вызывающий.
- MCPshka по-прежнему не в группе docker.
