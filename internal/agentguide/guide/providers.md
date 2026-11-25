# Площадки (провайдеры)

| Площадка | Роль | Правило |
|---|---|---|
| GitHub Actions | основной CI и сборка ARM64-релиза | минуты ограничены (2000/мес); черновые PR CI не запускают — помечай PR готовым только для релиза |
| GitVerse CI | второй полный CI (`release-suite`) | `forge_ci provider=gitverse mode=publish commit=<sha>` (любой коммит, ветки тоже) → `forge_ci mode=status` → после прогона `integration_test_prepare evidence=gitverse` |
| MosHub | третья независимая площадка (HTTPS, проверенный TLS) | `forge_ci provider=moshub mode=publish/status`; SSH не доверяем (ключи хоста не совпадают с опубликованными) |
| GigaIDE Cloud | разовые тяжёлые прогоны | **только бесплатная квота (~60 ч/нед); платное запрещено владельцем** — MCPshka откажет сама |
| VPS 203.0.113.10 | внешняя проверка доступности, лёгкие задачи | 1 vCPU / 1 ГБ — не для тестов MCPshka |
| H96 | деплой, verify, изолированные тесты проектов | прод: тяжёлое — по одному, с низким приоритетом |

Состояние и остаток квот — `providers_status`. Где у площадки нет API (запуск GigaIDE, секреты GitVerse) — `browser_run` с сохранённой сессией владельца. Офлайн-образ CI для MosHub собирает GitVerse CI (`moshub-ci-image.yml`, секрет `MOSHUB_REGISTRY_TOKEN`), MosHub берёт его из переменной `MOSHUB_CI_IMAGE`.

**Клиенты MCPshka.** ChatGPT — через туннель OpenAI (учётка `openai-tunnel`). Claude (claude.ai, приложения) — коннектор `https://h96-host.example.ts.net/mcp` через Tailscale Funnel: OAuth, каждое подключение подтверждает Face ID владельца, токен 24 ч с обновлением, действует от той же учётки (её отзыв отключает коннектор). Claude Code и агенты на H96 — локально `127.0.0.1:8765/mcp`. Админ-действия у всех требуют отдельного окна Face ID (`admin_session_*`). **Каталог (0.14.0):** обычный вход `…/mcp` отдаёт в tools/list ~50 основных инструментов (файлы, git, задания, проект, roadmap, карточки, память, CI/выпуск, Face ID, агенты, хост и дом); остальные ~240 зарегистрированы и вызываются `tool_resolve(goal)` → `tool_invoke(name, arguments)` (или `code_run`) с живыми схемами и теми же проверками. Компактный вход `…/mcp?tools=core` — 6 инструментов шлюза; снимок списка у клиента не устаревает от релизов.
