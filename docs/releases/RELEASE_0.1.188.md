# MCPshka 0.1.188

- BUG-20261024-106: `docker_smoke` в 0.1.187 падал на `mkdir /var/lib/mcpshka-docker-smoke: read-only file system` — брокеру разрешена запись только в `/var/lib/mcpshka-executor`. Состояние прогонов перенесено в `/var/lib/mcpshka-executor/docker-smoke`.
