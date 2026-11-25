# MCPshka 0.1.169 — тесты без GitHub и полный каталог для ChatGPT

Включает невыпущенную 0.1.168 (Actions исчерпан 2026-10-22, деплой без GitHub — отдельной частью).

## Новое
- **`forge_ci`** — прогнать любой коммит MCPshka на второй/третьей площадке без минут GitHub: `publish` кладёт неизменяемый снимок `mcpshka-snapshots/<commit>` на GitVerse или MosHub (там стартует их CI), `status` читает результат. Снимок больше не привязан к HEAD main: скрипты принимают `--commit` для любого коммита общей базы объектов, в том числе веток worktree. Пройденный GitVerse-прогон запечатывается для интеграции `integration_test_prepare evidence=gitverse` (BUG-49).
- **`tool_invoke(name, arguments)`** — вызвать любой инструмент по имени, если его нет в снимке `tools/list` клиента. ChatGPT держит только часть каталога из ~200 инструментов: `integration_publish_main` был зарегистрирован и разрешён, но не вызывался (BUG-51). Проверка прав, Face ID, ключ повтора, квитанции и допуск по нагрузке выполняются ровно один раз, как при прямом вызове; аргументы проверяются по схеме инструмента.

## Исправлено
- **`host_triage`**: для юнита в цикле рестартов показывает последнюю ошибку из журнала и не предлагает рестарт, повторяющий сбой (системный dnsmasq против адреса LANE, BUG-47); не предлагает `journal_read` для юнитов вне allowlist — все такие действия отказывали (BUG-48).
- Из 0.1.168: окружение проекта собирается через бинды юнита (BUG-43); уникальные id ночных прогонов (BUG-45); лимит сессии Claude Code распознаётся как квота агента (BUG-46).

## Локальный релиз без GitHub (BUG-20261022-44)

BUG-20261022-44: подготовка локального релиза при недоступном GitHub Actions.

### Квитанция и проверка

Добавлена `mcpshka.operator-verified-local/v1`, `provider=h96-local`.
Она всегда описывает пару controller/broker и содержит `repository`,
`source_sha`, `head_sha`, `tree`, `source_digest`, `build_permit`,
`build_env_digest`, `binary_sha256`, `broker_binary_sha256`, `test_evidence`.
Для локальной сборки `source_sha == head_sha`. Поля GitHub run/artifact
не подделываются: совместимый paired release-plan/v2 использует `ci_run=0`.
GitHub-квитанции v1/v2 сохраняют прежние проверки и положительный run ID.

`test_evidence` имеет строго поля `kind`, `id`, `tree`, `conclusion`, `suites`.
`id` — 32-hex permit успешного **fenced** исполнения, а не заявленный клиентом
номер CI. Допустимы только:

- `gitverse`: `release-suite=success`, `snapshot-contracts=success`; существующий
  integration flow проверяет неизменяемый snapshot коммита через
  `snapshot_immutable.py`/`ci_status.py` и серверный `cievidence`.
- `module`: `module=success`, результат sealed offline `go test ./...`.

У обоих вариантов `conclusion=success`, tree совпадает с `plan.source_tree`.
Root-производитель квитанции дополнительно проверяет одинаковые repository и
commit двух sealed permits. Build permit не принимается за test evidence.
Gate сверяет source digest и оба бинарника с plan, запрещает неизвестные поля,
неверные типы, пустые/неполные suites и неизвестный evidence kind.
Квитанция остаётся operator-verified; сама по себе не выдаёт deploy authority.

Гейт не верит квитанции на слово: копия, запечатанная root-воркером executor в
`/var/lib/mcpshka-executor/release-artifacts/<permit>` (каталог root 0700, файлы
0400), должна совпасть с присланной во всех полях кроме `test_evidence`, оба
бинарника перехешируются, разрешение сборки не может быть своим же доказательством тестов.

### Подготовка после установки согласованного комплекта

1. Подготовить и завершить test evidence для точного коммита:
   `integration_test_prepare evidence=gitverse` либо `evidence=module`.
   Для GitVerse предварительно опубликовать неизменяемый снимок.
   Запустить permit, дождаться результата и выполнить fencing через существующий
   `executor-canary launch|status|canary-fence`. Нужен `SUCCEEDED`, не просто exit 0.
2. В активной административной Face ID сессии вызвать `release_build_prepare`
   с `admin_session_id`, `path`, полным `commit`, `idempotency_key` и при
   необходимости `attempt` (1–9). До `SEALED` опрашивать с новым retry key.
   Сохранить `permit_id`, `launch_id`, `manifest.source.release_digest`.
3. Вызвать `release_prepare` с `source=local`, `version`, `current_version`,
   `source_digest`, `build_permit`, `launch_id`, `test_permit`, `idempotency_key`.
   URL, artifact path и caller receipt в этом режиме запрещены.
4. Наблюдать `release_status`. Job запускает только ещё не запущенный permit,
   ждёт результата, выполняет fencing и получает root-sealed бинарники
   ограниченными порциями. При потерянном ответе сначала сверить тот же permit;
   intent/UNKNOWN не означает разрешение на повторный запуск.
5. Результат — candidate, paired plan, local receipt и coordinated-upgrade manifest
   в существующем staging. `trust=UNTRUSTED`, `deploy_authorized=false`.
   Подготовка ничего не устанавливает и не исполняет candidate.

Сборка использует sealed снимок коммита и скопированный кэш модулей; сеть
закрыта systemd/bubblewrap, `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`,
`GOWORK=off`, `GOFLAGS=-mod=readonly`. Последовательно выполняются те же
`go build -p 2 -trimpath -buildvcs=false` для `./cmd/mcpshka` и
`./cmd/mcpshka-broker`, что в CI release-arm64. На H96 используется native
`CC=gcc`, `GOOS=linux`, `GOARCH=arm64`, `CGO_ENABLED=1`; в GitHub — cross compiler.
Одинаковая команда не обещает побитового совпадения разных toolchains.

`source_digest` использует тот же набор путей и framing, что
`packaging/cloud/source_digest.py`. `build_env_digest` — SHA-256 от строки
`go version` (включая завершающий LF), NUL-разделённых фиксированных GOFLAGS,
GOOS, GOARCH, CGO_ENABLED, CC и digest sealed кэша модулей. Digest кэша —
существующий `snapshotDigestLimited` (отсортированные пути и SHA-256 файлов).
Отсутствующие offline зависимости приводят к ошибке, сетевого fallback нет.

Профиль наследует ограничения module executor: Nice=19, idle I/O, CPUQuota=200%,
MemoryMax=1600M, TasksMax=256, срок исполнения до часа. Admission учитывает
занятый scratch, память и load не выше 1,5 на CPU. Исходники и cache read-only;
после сборки временный scratch очищается. Root-sealed бинарники/receipt
сохраняются как evidence под permit до отдельной проверенной уборки.

### Что требуется от владельца

Нужен **координированный апгрейд root-хелперов**, а не подмена одной Python gate:
новый controller (MCP API и native preparation), broker/executor (профиль сборки,
подготовка permit, чтение sealed результатов), `release-gate.py` и совместимый
`coordinated-upgrade.py`. `self-deploy.sh` не меняется, но входит в проверяемый
комплект с pinned digest. Старые установленные helpers не понимают новый
протокол и должны оставлять HOLD.

Владелец должен согласовать административное окно/Face ID, точный проверенный
коммит и SHA-256 полного комплекта, текущие installed pins и rollback-комплект;
затем отдельно разрешить bootstrap-установку согласованного набора root helpers.
Первый переход нельзя провести local-квитанцией через старую GitHub-only gate:
это bootstrap, требующий уже существующего доверенного пути установки.
После установки проверить новый API и настоящий sealed smoke на H96,
раздельно согласовать подготовку/продвижение production-релиза.

В этой задаче установка helpers, изменение production и деплой не выполняются.
Проверки используют тестовые fixtures; полный ARM64 build в живом sealed runtime
и bootstrap на H96 должны быть проверены в согласованном окне владельца.
