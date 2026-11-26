# MCPshka 0.2.1 — Tier 1

Первый выпуск ветки 0.2: закрыт Engineering-уровень Tier 1 (P0–P9). Вбирает несостоявшийся 0.1.197.

**Живучесть управления (P4, P0)**
- Два независимых OOB-пути, не зависящих от mcpshka, Cloudflare-туннеля и LANE: обратный SSH на стенд-VPS (`mcpshka-oob-tunnel`, 127.0.0.1:42022 на VPS, OpenVPN VPS не затронут) и Tailscale; вход — ключ с `from=` и принудительной командой `mcpshka-oob.py serve <путь>` (status/probe/receipt/reconnect/restart/rollback). `rescue_status` считает пути по свежим входящим вызовам (сейчас 2).
- `mcpshka-rescue` установлен (сторож туннеля каждые 15 с), доказательства OOB — `/var/lib/mcpshka-oob`.
- Chaos на стенде (`packaging/lab/canary_chaos.py`): kill -9 посреди мутации (задача сверена, тот же ключ — та же задача, запуск один), перезагрузка с переоткрытием состояния, нехватка места (мутация отклонена, после — чисто), обрыв транспорта.
- Lease: common Git dir; свой устаревший lease → явный `project_lease_heartbeat` вместо HOLD_REASON_UNCLASSIFIED; recovery preview — AUTH_OK/OWNERSHIP_BLOCKED/DEPLOY_FENCED/FENCE_REQUIRED.
- Ежедневный шифрованный бэкап состояния (04:30), повтор при rate limit Google Drive; бэкап 26.10 проверен с восстановлением 9864 записей. Архив репозиториев — открытый tar с git bundle (открывается без ключа).

**Подтверждения Face ID (все программы и устройства)**
- Ссылка approved_task/webos_deploy/коннектора/HA, открытая позже 5 минут, больше не даёт «passkey session invalid or expired»: план ждёт до 1 ч, челлендж перевыпускается при открытии и перед нажатием.

**Песочница и политика (P2)** — E2E изоляции на H96 PASS (python/bash/make/npm/go-test, попытки выхода host mount/network/proc заблокированы).

**Workflow и Health (P7, P8 — Codex)** — cold-bootstrap миссий, durable dispatch и outbox завершения, миссия не DONE до приёмки, версионированные workflow бэкапа/restore-плана, обнаружение дрейфа схем клиента, health: хранилище (свежесть бэкапа, реплики), каталог (manifest, индекс, missing actions), бюджеты опроса; матрица надёжности в `docs/WORKFLOW_HEALTH_ACCEPTANCE.md`.

**Исправления** — storage/backup падали на rclone-симлинке; tool_forge audit без ложных «unused»/overlap; HA: YAML-мост «LG TV HA» больше не пересохраняется при чтении, мост Z2M не устройство; webos_test называет причину; publish_main проверяет права `.git` до fast-forward; health предупреждает о размере ROADMAP; Codex: TLS error-doc, BUG-92/96/99/110, рефакторинг mcpserver (server.go 707→329, contracts.go 583→240).
