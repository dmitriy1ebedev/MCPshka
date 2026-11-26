# Тяжёлые проверки вне H96: CI GitVerse и раннеры

Одинаково для любого ИИ (ChatGPT, Claude, GigaChat, Kimi, DeepSeek, Gemini, Codex…): всё делают инструменты MCPshka, без SSH, git push и доступа к облаку с твоей стороны. H96 слабая — полный прогон большого проекта уноси отсюда.

Проще всего: `phase_gate` при отсутствии теста HEAD сам называет одно следующее действие (CI идёт → `forge_ci mode=wait` + `job_wait`; CI упал на самой площадке → `mode=rerun`; есть CI → `mode=publish`; живой раннер → `runner_task`; иначе `project_test`). Выполни ровно его.

Точечный тест (`project_test packages=[…]`, без `run`) уходит с H96 сам: MCPshka пинит рабочее дерево снимком-коммитом, ставит задание раннера (`job.id` вида `rj-…`) и ждёт живой раннер; нет его — будит временный раннер площадки (GitHub → GitVerse → MosHub → GigaIDE, первой — та, что дольше не работала; отказавшая отдыхает 30 мин), каждые 4 мин следующую; за 12 мин никто не взял — тот же тест запускается на H96 под тем же id. Ждать — обычный `job_wait`, вывод — `job_output`; `queue.reason` говорит, где идёт тест. `local:true` — сразу на H96.

Порядок выбора для полного прогона (тот же, что в rules п. 14 и в `phase_gate`): CI GitVerse проекта (есть `.gitverse/workflows`) → живой раннер (`runner_status`: `last_seen` < 2 мин, проект на Go) → `project_test` на H96 последним. `phase_gate` засчитывает любой из трёх: зелёный CI GitVerse, `runner_task profile=go-test` без packages (весь модуль, `DONE`) или sealed `project_test` того же HEAD.

## Путь 1 — CI GitVerse (основной, для любого проекта)
1. Закоммить (`project_commit`), возьми полный SHA.
2. `forge_ci provider=gitverse mode=publish path=/root/repos/<проект> commit=<sha> idempotency_key=<ключ>` → `job_wait` → `job_output`: MCPshka сама создаёт ПРИВАТНЫЙ репозиторий `<проект>` на GitVerse (публичный — отказ), ставит его `main` ровно на этот коммит (зеркало; история хранится на H96) и CI стартует.
3. `forge_ci provider=gitverse mode=wait path=… commit=<sha>` → `job_wait` в цикле, пока задание идёт (до 30 мин, одно задание держит весь прогон) → `job_output`: `state` PASS / FAIL, задачи и выдержки ошибок. Не заканчивай ход и не докладывай, пока ждёшь: делай следующую независимую задачу. `mode=status` — разовый взгляд.
4. `infra:true` в ответе status — упала сама площадка (HTTP 504 при загрузке Python/Go, DNS), а не код: `forge_ci provider=gitverse mode=rerun path=… commit=<sha>` — тот же коммит, новый прогон (нужен `on: workflow_dispatch` в workflow; повторный publish того же коммита прогон НЕ создаёт). Ответ `UNKNOWN` — не повторяй, читай `mode=status`.
5. `workflow:false` в ответе publish — в проекте нет `.gitverse/workflows/ci.yml`: создай его (`fs_write` сам создаст папки; `on: push: branches: [main]`, шаги теста проекта), закоммить, опубликуй снова.
Сама MCPshka идёт своим путём (`mcpshka_guide topic=release`), не через path.

## Путь 2 — раннер (облачная IDE или ноутбук владельца)

### Когда
- Нужен `go test`/`go vet`/`go build` большого проекта, а H96 занята (`host_triage` → pressure не PASS, очередь `project_test` длинная).
- Хочется быстро найти падения, пока H96 делает другое.

Раннер — облачная IDE (GigaIDE Cloud на GitVerse, GitHub Codespaces) или ноутбук владельца. Он сам ходит к MCPshka по HTTPS и берёт по одной задаче. Подключает его владелец на веб-странице «Раннеры» одной строкой `curl …/runner/setup | sh -s <токен>` (ключ хранится в рабочей папке IDE, автозапуск ставится сам); ИИ раннер не подключает.

### Как
1. Закоммить код: `project_commit` → возьми полный SHA (40 символов).
2. `runner_task path=/root/repos/<проект> commit=<sha> profile=go-test packages=["./internal/x"] idempotency_key=<новый ключ>` (профили: `go-test`, `go-vet`, `go-build`; без packages — `./...`). Ответ сразу: `job.id` вида `rj-…`.
3. Раз в ~60 с: `runner_status job_id=rj-…` до конечного состояния (в паузах — независимая работа, не доклад владельцу):
   - `DONE` — прошло (exit 0);
   - `FAILED` — смотри `output` (хвост вывода) и `error`, чини, снова коммит и шаг 2;
   - `NO_RUNNER` — ни один раннер не взял задачу за 10 мин: просто запусти то же через `project_test` на H96. Это не блокер. Если ответ `runner_task` уже говорит `queued, but live runners lack …` или `no runner seen` — не жди 10 мин, сразу CI или `project_test`.
4. `runner_status` без аргументов — какие раннеры живы (`last_seen`, `caps`), чем заняты.

### Важно
- `DONE` полного `go-test` (без packages) засчитывает `phase_gate`; вливание MCPshka в main по-прежнему требует GitVerse CI или sealed-теста H96.
- Раннер исполняет только профиль, а не произвольную команду; код — ровно указанный коммит.
- Раннер уснул посреди задачи (GigaIDE засыпает после часа простоя) — задача сама вернётся в очередь (до 3 попыток). Ничего чинить не надо.
- Раннер сам выходит после 10 мин без задач (`--idle-exit`), чтобы облачная IDE уснула и не тратила бесплатные часы; при следующем запуске пространства его поднимает автозапуск. Нет раннера 10 мин → задача `NO_RUNNER` → тот же прогон `project_test` на H96.
- Инструменты тестов (sqlite3, PyYAML) агент раннера v3 ставит сам без sudo в свою папку; старый агент обновится при следующем запуске IDE.
- Отключить раннер может только владелец на странице «Раннеры».
