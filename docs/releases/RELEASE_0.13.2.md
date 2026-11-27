# MCPshka 0.13.2 — 2026-11-15

## Сеть важнее сборок (P5.3/P5.4, Codex)
- Общий `mcpshka-heavy.slice` (Nice 10, IOSchedulingClass idle) для сборок, тестов, агентов и воркеров исполнителя.
- Домашние службы находятся по живому хосту (docker, containerd, dnsmasq, tailscaled, laned, lane-*), без списков руками.
- `mcpshka-broker home-priority --startup|--rollback`: drop-in-ы с резервной копией + `systemctl set-property --runtime`, `daemon-reload`, службы не перезапускаются. Встроено в coordinated-upgrade (откат при неудаче) и install-broker.sh.
- `project_verify`: проверка `home_priority` падает при расхождении.

