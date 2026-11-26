# MCPshka 0.1.189

Autopilot, этап 1 — готовность возможностей и self-test.

- Готовность возможностей: у каждой возможности состояние `registered → preflight → ready / degraded / blocked`, контракт (эффект, привилегии, каталоги, лимиты, recovery) и зависимости от источников `health_snapshot`. Без свежих данных — `preflight`, никогда не `ready` (fail-closed).
- `capability_selftest names=[…]` — проверить возможности сейчас: источники здоровья + собственный self-test от UID, который её обслуживает. Первый self-test — `docker_smoke` от брокера: запись/удаление пробного файла в `/var/lib/mcpshka-executor/docker-smoke`, helper воркера, `systemd-run`, heavy queue, dockerd.
- `capability_preflight capabilities=[…] step=…` — dependency preflight шага roadmap: регистрация → права → зависимости и self-test → незавершённый recovery; вердикт и первый блокер с `next_safe_action`.
- `tool_resolve` и `capabilities_bootstrap` отдают настоящие `ready` / `backend_healthy` (раньше всегда `unknown`); в bootstrap — счётчики по состояниям.
- BUG-20261025-107: `docker_smoke` сразу после старта отвечал FAILED «worker ended without a result» — `systemctl is-active --quiet` не считал живым `activating`. Теперь живыми считаются переходные состояния и свежий STARTING (2 мин, пока systemd не загрузил юнит).
