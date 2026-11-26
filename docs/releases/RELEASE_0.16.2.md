# MCPshka 0.16.2 — 2026-11-18

Исправление 0.16.1: `forge_ci provider=distributed mode=plan` в проде падал «bounded source command failed» — `go list ./...` во временном снимке коммита (git archive, не checkout) не мог получить VCS-статус. Теперь `GOFLAGS=-mod=readonly -buildvcs=false`, ошибка показывает команду и последнюю строку stderr. Тест воспроизводит снимок с битым `.git`.
