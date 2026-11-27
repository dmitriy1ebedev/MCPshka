# MCPshka 0.1.182 — облачные агенты Codex, рабочий Wi-Fi скан для DIAG

- C1 (Codex): `cloud_agent_task` — запуск задачи Codex Cloud по неизменяемому снимку репозитория под Face ID, опрос состояния, durable SQLite-квитанции, идемпотентность, проверка результата (ветка/PR). Живой прогон требует однократной настройки окружения владельцем: `sudo /usr/local/libexec/mcpshka-cloud-agent.py configure ENV_ID` (см. docs/CLOUD_AGENTS.md). Облачный Claude Code пока HOLD.
- BUG-20261024-102: `diag_wifi_scan` из 0.1.181 не работал — семейство брокера `wifi-scan` отсутствовало в списке клиента. Исправлено, с тестом.
