<h1 align="center">MCPshka</h1>

<p align="center">
  <img src="https://img.shields.io/badge/version-0.19.4-brightgreen" alt="version 0.19.4">
  <a href="https://github.com/dmitriy1ebedev/MCPshka/actions/workflows/open-core.yml"><img src="https://github.com/dmitriy1ebedev/MCPshka/actions/workflows/open-core.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go" alt="Go 1.26">
  <img src="https://img.shields.io/badge/platform-linux%20arm64%20%7C%20amd64-lightgrey" alt="Linux arm64 | amd64">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache-2.0"></a>
</p>

<p align="center"><a href="README.md">English</a> · <b>Русский</b></p>

**MCPshka** — собственный [MCP](https://modelcontextprotocol.io)-сервер, через который ИИ-чаты (ChatGPT, Claude, GigaChat и другие) безопасно управляют Linux-машиной: запускают задачи, правят код, гоняют тесты, выпускают версии и управляют умным домом. Вывод жёстко ограничен, всё пишется в журнал, а опасное подтверждает владелец по Face ID.

Это один нативный Go-бинарь для маленькой ARM64-приставки, работающей круглосуточно. MCPshka разрабатывает сама себя: ИИ, подключённый к MCPshka, пишет, тестирует, вливает и устанавливает её следующий выпуск её же инструментами.

## Возможности

- **Ограничения с самого начала** — любая команда становится задачей; вывод читается курсором с лимитом на сервере, контекст модели не переполняется.
- **«Удалённый рабочий стол» для ИИ** — процессы, файлы, поиск и точная правка в духе Desktop Commander, через stdio или Streamable HTTP.
- **Владелец решает** — разрушительные, привилегированные действия и выпуски требуют одноразового подтверждения WebAuthn (Face ID); токены хранятся только хэшами.
- **Один писатель на проект** — аренда рабочей копии, ключи идемпотентности на каждое изменение, задачи переживают перезапуск.
- **Запечатанные CI и выпуски** — тесты в песочнице на хосте, очередь вливания с fast-forward, согласованное обновление с откатом.
- **Распределённый CI** — куски тестов расходятся по GitHub, GitVerse, SourceCraft и MosHub; если недоступны все площадки, хост прогоняет их сам.
- **Code Mode** — один JavaScript-сценарий `code_run` вызывает много инструментов параллельно вместо десятков ходов чата.
- **Дом и сеть** — Home Assistant, Docker и systemd по белым спискам; изменения сети идут через поэтапный провайдер с автооткатом.
- **Много «мозгов»** — GigaChat и бесплатные LLM через один шлюз с вырезанием секретов; Codex и Claude Code как агенты-исполнители.
- **Память** — локальная база знаний, которую ИИ вспоминает и дополняет между чатами.

## Содержание

- [Быстрый старт](#быстрый-старт)
- [Установка](#установка)
- [Подключение ИИ-клиента](#подключение-ии-клиента)
- [Настройка](#настройка)
- [Устройство](#устройство)
- [Безопасность](#безопасность)
- [Состояние проекта](#состояние-проекта)
- [Чем вдохновлялись](#чем-вдохновлялись)
- [Лицензия](#лицензия)

## Быстрый старт

Нужны Go 1.26+ и Linux.

```sh
git clone https://github.com/dmitriy1ebedev/MCPshka.git
cd MCPshka
go build -o mcpshka ./cmd/mcpshka
./mcpshka version
./mcpshka status        # краткое состояние хоста
./mcpshka mcp-stdio     # MCP через stdio для локального клиента
```

## Установка

Постоянная установка службой systemd (состояние в `/var/lib/mcpshka`, HTTP на `127.0.0.1:8765`, реестр токенов создаётся в `/etc/mcpshka`):

```sh
go build -trimpath -o bin/mcpshka ./cmd/mcpshka
sudo packaging/native/install.sh
```

Root-брокер для запечатанных тестов и выпусков ставится отдельно: `packaging/native/install-broker.sh`. Удаление — `packaging/native/uninstall.sh`. Docker поддерживается как необязательный режим изоляции.

## Подключение ИИ-клиента

- **Локальный клиент (stdio):** укажите клиенту команду `mcpshka mcp-stdio`.
- **Удалённый клиент (HTTP):** `MCPSHKA_TOKEN=… mcpshka mcp-http` слушает только loopback; наружу — через исходящий туннель с авторизацией, открывать порт на роутере не нужно. См. [docs/CLIENT_COMPATIBILITY.md](docs/CLIENT_COMPATIBILITY.md).

По умолчанию клиент видит около 40 инструментов; остальные ИИ находит сам через `capabilities_bootstrap` и `tool_resolve`.

## Настройка

| Переменная | Назначение |
|---|---|
| `MCPSHKA_HTTP_ADDR` | Адрес HTTP (loopback) |
| `MCPSHKA_AUTH_FILE` | Реестр токенов: хэши, роли, права, срок действия, ротация без перезапуска |
| `MCPSHKA_TOKEN` | Один bearer-токен (старый режим, если реестра нет) |
| `MCPSHKA_DEFAULT_ROLE_PROFILE` | `operator` (по умолчанию) или `reader` (только чтение) |
| `MCPSHKA_PROJECT_PROFILES` | Роли по проектам, JSON |
| `MCPSHKA_SYSTEMD_SERVICES`, `MCPSHKA_DOCKER_READ_CONTAINERS` | Белые списки служб и контейнеров |

Проект описывает свои сценарии тестов и сборки в `.mcpshka/profile.json` (схема `mcpshka.project-profile/v1`, прямой argv, без строк оболочки).

## Устройство

```
ИИ-чат ──MCP──► mcpshka (служебный пользователь) ──► задачи, файлы, git, проекты, CI
                    │
                    └─unix-сокет──► mcpshka-broker (root) ──► запечатанный исполнитель, выпуски, systemd
```

- `cmd/mcpshka` — MCP-сервер и CLI; `cmd/mcpshka-broker` — root-брокер; `cmd/ci-shard` — исполнитель распределённого CI.
- `internal/tools/*` — по пакету на семейство инструментов; MCP — адаптер, а не ядро.
- Подробности: [docs/INVARIANTS.md](docs/INVARIANTS.md), [docs/EXECUTOR_RUNTIME.md](docs/EXECUTOR_RUNTIME.md), [docs/DISTRIBUTED_CI.md](docs/DISTRIBUTED_CI.md).

## Безопасность

- HTTP слушает только loopback; удалённый доступ — через туннель с авторизацией.
- Классы команд (чтение, изменение, разрушение, привилегии) распределяют риск; разрушительное и привилегированное требует одноразового подтверждения владельца.
- Классы команд — не песочница: разрешённый интерпретатор может выполнить любой код, поэтому тестируемый код идёт в запечатанном исполнителе без сети.
- Секреты не попадают ни в вывод инструментов, ни в argv; журнал аудита вырезает аргументы.
- Публичное дерево выгружается из приватного репозитория с проверкой на секреты и личные данные; адреса и ключи владельца заменены документационными.

## Состояние проекта

Альфа: с осени 2026 года каждый день работает на одном домашнем хосте. API и имена инструментов ещё меняются между выпусками. Изменения — [CHANGELOG.md](CHANGELOG.md), заметки к выпускам — [docs/releases](docs/releases).

Зеркала: [GitHub](https://github.com/dmitriy1ebedev/MCPshka) · [GitVerse](https://gitverse.ru/dmitriy_1ebedev/MCPshka) · [SourceCraft](https://sourcecraft.dev/lda6994/mcpshka) · [MosHub](https://hub.mos.ru/lda6994/mcpshka).

## Чем вдохновлялись

MCPshka выросла из чужих хороших идей:

- [Desktop Commander MCP](https://github.com/wonderwhy-er/DesktopCommanderMCP) — модель «пульта для ИИ-чата»: сессии процессов, чтение окнами, точная правка.
- [Model Context Protocol](https://modelcontextprotocol.io) и [официальный Go SDK](https://github.com/modelcontextprotocol/go-sdk).
- [go2rtc](https://github.com/AlexxIT/go2rtc) — пример того, как один небольшой Go-бинарь держит целую домашнюю подсистему.
- [Home Assistant](https://github.com/home-assistant/core) — умный дом, которым управляет MCPshka.
- [Zuul project gating](https://zuul-ci.org/docs/zuul/latest/gating.html) — очередь вливания.
- [Temporal](https://github.com/temporalio/sdk-go) и [go-workflows](https://github.com/cschleiden/go-workflows) — долговечная работа с продолжением после сбоя.
- [Litestream](https://github.com/benbjohnson/litestream) и [age](https://github.com/FiloSottile/age) — резервные копии SQLite и их шифрование.
- [tusd](https://github.com/tus/tusd), [gobreaker](https://github.com/sony/gobreaker), [OpenTelemetry Go](https://github.com/open-telemetry/opentelemetry-go).
- [OpenAI Codex CLI](https://github.com/openai/codex) и [freellmapi](https://github.com/tashfeenahmed/freellmapi).
- Инструменты [Uncle Bob](https://github.com/unclebob): [crapper](https://github.com/unclebob/crapper), [mutator](https://github.com/unclebob/mutator) и [dryer](https://github.com/unclebob/dryer) — проверки здоровья кода.

## Лицензия

[Apache License 2.0](LICENSE). См. [NOTICE](NOTICE).
