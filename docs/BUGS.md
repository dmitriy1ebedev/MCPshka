# Баги MCPshka: как распознать, завести и закрыть

Реестр — таблица «Реестр багов» в `ROADMAP.md` (строка `BUG-ГГГГММДД-N`). Подробности (SHA, job, run, замеры) — сверху в `roadmap/EVIDENCE_LOG.md`. Этот документ — правила и готовые диагнозы для частых граблей.

## 1. Когда это баг MCPshka

Заводить, если **инструмент MCPshka** ведёт себя не так, как обещает описание, или заставляет обходить себя:

- отказ HOLD/UNKNOWN без названной причины или без пути восстановления;
- job/lease/deploy застрял и сам не разрешается (`unknown`, `busy` без живого процесса);
- ответ не проходит схему клиента (`validating tool output…`);
- чат вынужден идти через bash/RDC за тем, что должно делаться инструментом;
- модель в чате делает неверный вывод, потому что MCPshka не дала факт (тогда баг — «нет инструмента/факта», а не «модель ошиблась»).

Не баг MCPshka: состояние хоста по решению владельца (остановленный lane), внешний провайдер недоступен. Такие строки — со статусом `HOST` или `HOLD (внешний)`.

## 2. Как завести

1. Сначала факты, потом выводы: точный текст ошибки, инструмент и аргументы (без секретов), id job/permit/deploy, время.
2. Найти место в коде по тексту ошибки (`grep -rn "<текст>" internal`) — в строке реестра указать, где ломается.
3. Строка в реестре: `| BUG-ГГГГММДД-N | дата, контекст | что видно → почему | фазы | статус |`. Номер — следующий за последним в этот день.
4. Если баг блокирует работу прямо сейчас — сначала штатное восстановление (ремонт fence, `job_status` для сверки, отмена своего элемента очереди), и только потом фикс в коде. Защиту не обходить.

## 3. Статусы

| Статус | Значит | Когда ставить |
|---|---|---|
| `OPEN → vX` | понят, запланирован | есть причина и версия |
| `IN PROGRESS vX` | код пишется | ветка/исполнитель названы |
| `FIX-CANDIDATE vX` | исправление в кандидате | коммит есть, в проде ещё нет |
| `FIXED vX` | исправлено **и проверено в проде** | после `project_verify` **и** повторения исходного сценария |
| `PARTIAL vX` | часть исправлена | остаток описан и запланирован |
| `CLOSED (by design / superseded / mitigated)` | менять не будем | с объяснением почему |
| `HOST`, `HOLD (внешний)` | не код MCPshka | с условием возврата |

**Главная грабля реестра:** `FIX-CANDIDATE` не переходит в `FIXED` сам. После каждого деплоя пройтись по строкам этой версии и либо подтвердить повтором сценария, либо оставить с пометкой. Иначе через неделю «29 открытых», из которых половина давно исправлена.

## 4. Как закрыть

1. Тест, который падал бы без фикса (регрессионный), рядом с кодом.
2. CI зелёный, деплой, `project_verify`.
3. **Повторить исходный сценарий в проде** (тот же инструмент, те же входы) и записать результат в `EVIDENCE_LOG.md`.
4. Статус `FIXED vX — <одна фраза, чем подтверждено>`.

## 5. Готовые диагнозы (грабли 2026-10)

| Симптом | Причина | Что делать |
|---|---|---|
| `interactive fence unavailable: HOLD` при `project_lease_acquire` | lock-файл fence в `/var/lib/mcpshka/leases/` создан root (`root:root`) | `project_lease_admin_fence_preview` → `project_lease_admin_fence_repair` (Face ID), любой репозиторий с 0.1.163 |
| `fixed main repository required` при lease recovery другого проекта | старый broker ограничивал legacy recovery MCPshka | кандидат `codex/lease-recovery`: root worker проверяет регистрацию/common Git authority; Face ID и OS fence обязательны |
| lease `unverified`, даже когда тест завершён | интерактивный удалённый чат не имеет локального PID; executor pin теста независим | `project_context` и guide recovery; свой token → heartbeat/resume; неизвестный владелец → admin preview, возраст не доказательство |
| job `unknown`, `job_status`/`job_cancel` → `executor HOLD` | подготовка sealed-теста упала до выдачи permit | с 0.1.162 закрывается сам как FAILED через 2 мин (`abandon-recipe`); смотреть юнит `mcpshka-recipe-prepare-*` |
| root-юнит падает без сообщения на `chown`/`chmod` | у юнита нет `CAP_CHOWN`/`CAP_FOWNER` в `CapabilityBoundingSet` | сравнить с соседними юнитами (`integration_admin.go`), добавить права и тест на строку юнита |
| `setresuid … EPERM` в root-юните | `NoNewPrivileges` + seccomp без `AmbientCapabilities=CAP_SETUID CAP_SETGID` | добавить ambient caps |
| sealed-тест `timed_out` без вывода | тесной песочнице не хватает CPU/памяти/кэша Go | бюджет как у теста модуля (scratch на диске), с 0.1.163 |
| `validating tool output … want "null, array"` | `json.RawMessage` в выходном типе | с 0.1.163 схемы описывают его как любой JSON; новые типы — проверять тестом схемы |
| чат пишет «resource gate FAIL» при load 3.6 | абсолютный load сравнили с порогом на ядро | `phase_gate` / `diag pressure`: load / число CPU ≤ 1.5; нагрузка только откладывает тяжёлые job |
| чат пересдаёт гейты, уже PASS/WAIVED | не прочитан checkpoint фазы | `phase_gate path=… phase=Pxx` |
| тесты executor падают на H96, в CI зелёные | запуск от root | с 0.1.163 `TestMain` перезапускает от `nobody`; остаток — BUG-20261020-10 |
| деплой: `broker.sock: i/o timeout` | брокер не ответил под нагрузкой | `project_context` → если `deployment_pending=false`, повторить с **новым** ключом |
| Codex сжёг миллионы токенов | `codex exec resume` тянет весь старый контекст | новая короткая сессия с компактной задачей (`/root/cxgo3.sh`) |
| CI-минуты GitHub на исходе | каждый push в PR гоняет 3 workflow (~40 мин) | черновые PR не гоняют CI (0.1.163); сеть провайдеров — 0.1.164 |

## 6. Как не наступать снова

- Новый root-юнит → тест на полный список `--property=…` (права, bind, лимиты), как `recipe_executor_test.go`.
- Новый выходной тип с `json.RawMessage`/`any` → тест «схема принимает реальный ответ» (`raw_schema_test.go`).
- Любой путь, где job может остаться `unknown`, должен иметь доказуемое разрешение (FAILED/CANCELLED) без участия человека.
- Факт, который чат проверяет «на глаз», — вынести в инструмент с вердиктом (`phase_gate`, `host_triage`).
- После деплоя — пройтись по `FIX-CANDIDATE` этой версии (см. §3).

## Кандидат M1 — ветка codex/m1-025 (без выпуска)

- **BUG-20261026-137:** FAILED подготовки возвращает `reason`, `log_excerpt` (не более 4 KiB из последних 30 строк только своего вычисленного unit), `log_error` при недоступном журнале и `next_action`. Типичные credentials удаляются. Admission/fencing остаётся HOLD до штатной сверки; произвольное чтение unit по тексту агента не разрешено. Обычные проекты идут через ff-merge под lease + sealed project_test.

- **BUG-20261027-138 — автопродолжение квоты, M1 пункт 7 (кандидат codex/m1-025):** agent_task больше не завершает unit на quota с известным будущим reset_at. Ожидание reset + 1 мин освобождает heavy queue, successor связан resume_of/resumed_by. Попытки выполняются в отдельных bounded units; ожидание раннера не выдаётся за живой unit прежнего агента при recovery lease. Ограничения: 8 ч/3 продолжения; неизвестный reset требует evidence; после reboot — явный interrupted/manual resume. Registered Go tree snapshot теперь получает read-only зависимости из доверенного cache по committed go.sum (как module gate), offline-политика и прежние лимиты сохранены. Регрессия sealed полного теста: core129 Python fixture использует tempfile вместо отсутствующего /var/tmp, без изменения production helper.
- **COLLECTING в release_local:** протокол переносит образы по 32 KiB через broker, расходуя бюджет каждого клиентского вызова. Части и проверенные образы уже сохраняются между вызовами (BUG-66/74); число COLLECTING само по себе не доказывает зависание. Для актуального выпуска этот перенос не нужен: release_apply читает root-owned образы напрямую по sealed build_permit/test_permit, проверяет тот же protected main и сам раскладывает пару. Guide release переведён на этот маршрут; release_local оставлен для совместимости. Продовый повтор выполняет выпускающий после ревью.
- **M1:** roadmap_run принимает goal+path без ROADMAP; общий roadmap_run_status показывает активные/последние проекты и причины остановки. Слова агента не заменяют sealed project_test; неизвестный исход job требует сверки перед повтором. Эти изменения до ревью/деплоя остаются кандидатом, не FIXED.

## Кандидат P4 approval UX — ветка claude/p4-approval-ux (без выпуска)

Источник: реальная сессия ChatGPT → MCPshka/H96/LG 2026-10-26/27 (HA noise bridge, LG 55NANO926PB). Независимый read-only аудит — Codex agent_task `c8d33c6cf2a1ce6e` (подтвердил 1–7, добавил `-T`, NO_CHANGES, «ошибка systemctl ≠ остановка воркера»). Ревью коммита — Codex `43526ef881c746c8`: 2 MAJOR (none при ошибке systemd-run; ложный verified) и 3 MINOR (перепроверка под локами при продлении, HTTP без прокси, аннотация r2) — все исправлены.

**Не дефект MCPshka (EXTERNAL):** повторный `approved_task_prepare` с НОВЫМ idempotency_key иногда блокирует системная tool-safety клиента OpenAI («вызов инструмента заблокирован»). MCPshka этот вызов не получает и обойти не может. Смягчение — продление тем же ключом (ниже) не требует нового ключа.

- **BUG-20261027-139 — просроченное согласие approved_task:** задача навсегда `AWAITING_FACE_ID`, повтор тем же ключом возвращал старую, статус не отличал ожидание от истечения. Теперь `approval_state` (awaiting_face_id / authorized / approval_expired / authorization_expired / consumed_unrecorded), `approval_deadline`, `next_safe_action`; тот же ключ + тот же закреплённый план продлевает согласие: тот же id/plan_hash/URL, новый challenge, новый Face ID обязателен; ≤20 продлений. Другой план под тем же ключом или изменённые файлы — HOLD (раньше молча отдавалась старая задача).
- **BUG-20261027-140 — admin_coverage(approved_task_execute):** советовал `mutation_approval_request`/contract token, хотя у задачи свой Face ID на план. Теперь `needs: plan_face_id` и точный маршрут. Админ-сессия по-прежнему НЕ покрывает произвольный root-план — по замыслу.
- **BUG-20261027-141 — эффект FAILED:** установщик включил unit, но его финальный urllib из песочницы `network:false` упал → FAILED без признака эффекта. Теперь `effect`: applied / none (только провал проверки пинов) / unknown (всё прочее, включая ошибку systemd-run). План может закрепить `units` и `probe` (http://<частный IP>:<порт>/путь, без прокси и редиректов); `approved_task_status` показывает наблюдения хоста (`verify[]`, `observed: declared_state_present`), но эффект не повышает — по ревью Codex `43526ef881c746c8` активный unit мог существовать до запуска. Повтор unknown запрещён в next_safe_action.
- **BUG-20261027-142 — service_status установленного unit:** POLICY_DENIED для `go2rtc-ha-noise.service`. Юниты из `units` выполненного плана записываются брокером (root, ≤64, task+plan_hash) и доступны только для status/list (`owned_by`); restart, журналы, mcpshka* — нет.
- **BUG-20261027-143 — webOS без preflight:** Face ID тратился на выключенный ТВ. `webos_deploy` закрепляет `reach: [<tv>:9922]`; `approved_task_execute` проверяет reach ДО расхода согласия → HOLD «approval NOT spent». `webos_test`: неинтерактивный ssh с `-T`, отказ PTY больше не маскирует исход обычного вызова.
- **BUG-20261027-144 — потерянный worker:** пустой ответ `systemctl is-active` (ошибка самого systemctl) считался остановкой → ложный FAILED. Теперь только состояние systemd; исход потерянного worker — effect unknown.
- **BUG-20261027-145 — exec_start node:** отказ «class privileged» без пути. Для интерпретаторов/сборщиков подсказка `project_test`/`project_build`/`project_run`; права не расширены.
- **BUG-20261027-146 — root_task_prepare при staged == live:** готовил пустую задачу. Теперь `state: NO_CHANGES` и путь стейджинга. Направленная правка configuration.yaml / REST command — **OPEN (дизайн)**: нужен schema-typed writer с backup/CAS/ha_check_config/reload/rollback; произвольный root YAML не вводится.
- **BUG-20261027-147 — гейты на sibling worktree:** `phase_gate` на `/root/repos/mcpshka-claude-p4ux` → `git status: dubious ownership` (MCPshka читает git не от владельца worktree). Sealed `project_test` в проде 0.2.4 запускает Go с пустым GOMODCACHE и `GOPROXY=off` → `setup failed` у всех пакетов с внешними модулями (исправлено в 0.2.5, BUG-138, ещё не в проде). OPEN. 27.10: 0.2.5 в проде; ветка перенесена в сервисный worktree через `git checkout` от mcpshka (root-созданный worktree удалён).
- **BUG-20261027-148 — project_repo_adopt всегда HOLD:** проба контракта `project_repo_preview` не получала аргументов → брокер `project-adopt requires session, owner, action, repository and source` до любого действия (так упал и handoff Codex perf 2b28ef5d). Исправлено: проба передаёт admin_session_id/path (source только до действия). FIXED в ветке.
- **BUG-20261027-149 — sealed project_test зарегистрированного Go-дерева в 0.2.5 не готовится:** юнит `mcpshka-recipe-prepare-*` прячет `/root` (TemporaryFileSystem) и монтировал только `/root/repos`, а 0.2.5 копирует go.sum-зависимости из `/root/.local/share/go/pkg/mod` → exit 1 без текста, job `external start outcome unknown` (mvqg88zd-6 на 1ecd97a). Подготовка дерева без модулей уже занимала 28 с из 30-секундного бюджета брокера при CPUQuota=50%. Исправлено: кеш монтируется read-only (`-` — терпит отсутствие), CPUQuota=100%. Проверка — sealed project_test после установки. OPEN: причина отказа подготовки не доходит до job (как BUG-137).
- **OPEN:** sealed build → IPK validation → deploy одной цепочкой с SHA-пинами (составной approved_task ранее «worker ended without a result»); пока — раздельные шаги + effect unknown без слепого повтора.

Порядок работы оператора — `docs/APPROVED_TASK_WORKFLOW.md`.
