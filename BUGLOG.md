# Журнал багов MCPshka

Как заводить и закрывать — `docs/BUGS.md`; рабочий реестр — таблица «Реестр багов» в локальном ROADMAP.
Здесь — итог по выпускам: симптом → причина → где исправлено.

## 0.19.1, 25.11.2026

| ID | Симптом | Причина | Исправлено |
|---|---|---|---|
| MCP-278 | roadmap_close отказывал PASS-пунктам S5.13, S5.24–S5.30, K2.x | разбор пунктов не видел `###` внутри потоков S/C | 0.19.1: roadmapscan.ItemHeadings |
| MCP-256 | каждый systemctl через брокер 3,08 с, response_write_failed, BROKER_CIRCUIT_OPEN у клиента | от root unitbus шёл в приватный сокет systemd; поток сигналов transient-юнитов задерживал ответ GetAll дольше 3 с (исправление 0.19.0 — одно соединение на вызов — не помогло) | 0.19.1: системная шина первой |
| — | 25.11 04:46–06:25 дом без Zigbee: zigbee2mqtt exited | у контейнера умер shim, рестарты Docker не прошли и оставили exit 0 с State.Error; selfheal считал это docker stop | 0.19.1: State.Error = сбой |
| — | mcpshka-state-backup failed («bounded backup operation failed»), за ним lane-postcheck | Python-помощник ограничивал базу 64 МиБ, живая база 67,8 МБ; логи заданий тоже шли в общий бюджет | 0.19.1: бэкап на Go внутри брокера (Python-помощник с хоста выпуском не обновлялся), логи в бюджете 32 МиБ |
| — | единственная строка FAIL sealed-прогона пропала в вырезанной середине | бюджет вывода head/tail | 0.19.1: строки сбоев сохраняются |
| — | brain_ask отвечал по обрезанному JSON | обрезка результата на 6 КБ посередине | 0.19.1: compact JSON |
| — | FreeLLM «auto» пропадал из маршрутизатора | фильтр serving() сверял только Model | 0.19.1: учитывается BrainModel |

## 0.17.2 на H96, 22.11.2026

Владелец вручную установил MCPshka 0.17.2 из коммита `e39e45e586ebb865c8e8c529faacb25b24ddd9a8` (мерж PR №107). Штатный путь `integration_approve → release_local` был заблокирован состояниями `REVOKING` / `FENCED_UNKNOWN`. Нативная сборка выполнена с `CGO_ENABLED=0`, `-trimpath -buildvcs=false -ldflags '-s -w'`.

На H96 сверены версия, контрольные суммы и состояния `mcpshka-broker`, `mcpshka`, `mcpshka-tunnel`: все три службы активны, `NRestarts=0`. `release_apply_status` подтверждает запущенную 0.17.2 и сохраняет `HOLD` из-за отсутствия свидетельства штатной установки. `phase_gate` прошёл для локального `main` на `ffaf95e`.

| Бинарник | SHA-256 установленного файла |
|---|---|
| `mcpshka` | `023eb329a02bda99b52ddf500dafcfe802eda9426e96a11554cd096abf10db4e` |
| `mcpshka-broker` | `ad994e14f1f62a791af8627398e722717712de598a4e5d1c39efb010cb0bb507` |

| ID | Симптом | Причина | Исправлено |
|---|---|---|---|
| MCP-190 | `release_apply` после удачной установки: «staging failed», причина пустая | coordinated apply перезапускает брокер, пока тот ждёт pipe stage-юнита | 0.13.1: отчёт воркера в /var/lib/mcpshka-release-apply/<v>.json, брокер отвечает из него |
| — | дома «ничего не грузит», Instagram/YouTube рвутся, load H96 14 | сборка выпуска DIAG шла без пониженного веса CPU и мимо очереди тяжёлых задач, рядом тесты агента и zigbee2mqtt | 0.13.0: юнит DIAG CPUWeight=20/IOWeight=10, `heavyqueue.Run`; сетевые службы H96 — CPUWeight=1000 (drop-in) |
| — | `agent_task` с путём worktree → POLICY_DENIED при согласии на репозиторий | preflight сверял сырой путь, не репозиторий worktree | 0.13.0: `agentscope.ScopeArgs` в admission |
| MCP-188 | ChatGPT топтался: `project_run` POLICY_DENIED без маршрута, `webos_deploy` не видел собранный IPK, approved_task отвергал реальный `/root/repos/...` как «нет пути» | нет next_safe_action; deploy читал только репозиторий; брокер с ProtectHome=tmpfs проверял пути записи сам | 0.13.0: `next_safe_action: project_pipeline`, экспорт пайплайна HEAD, проверка путей в инспекторе (п. 4 — ссылки `pipeline:` — следующим выпуском) |
| MCP-173 | Termius на iPhone закрывался сразу после входа по SSH на H96 (сервер видел сброс соединения через ~2 с; вход по паролю из другого клиента с тем же MOTD работал) | причина на стороне клиента не доказана; сводка входа собиралась синхронно внутри PAM (1,5 с: systemctl, docker, journalctl, сеть) и уходила клиенту с цветами ANSI и значками | `packaging/native/h96/motd`: вход печатает готовый снимок (только ASCII и кириллица, ≤ 2000 байт, 0,01 с), сбор — отдельным фоновым заданием с пределом времени, полная сводка — команда `motd`; установка и откат — `install.sh apply|rollback` от root. Закрывается проверкой Termius владельцем |
| BUG-20261103-1 | вливание падало «fast-forward outcome UNKNOWN» из-за неотслеживаемого файла в основном каталоге; HOLD «checkout is not on the integration target» шёл без класса | не было предпроверки неотслеживаемых файлов и типа причины | причина называется с 0.3.1 (`untrackedCollisions`, `CHECKOUT_NOT_ON_TARGET`); строка реестра закрыта в 0.11.5 |
| — | `fs_find`: одна недоступная папка (`.agents`) отменяла весь поиск, тайм-аут ripgrep возвращал ошибку без найденного | обход прерывался на первой ошибке, два движка с разным поведением | 0.11.5: один проход, `partial`/`timed_out` с курсором, счётчики пропусков |
| — | `mcpshka memory-questions` на проде: «memory sqlite: signal: killed» (в лаборатории проходило) | каждая запись вопросов пересобирала индекс всей памяти одним вызовом sqlite с бюджетом 8 с; на нагруженном H96 дольше | 0.11.4: переиндексация только затронутых записей, полной — 2 мин |
| MCP-168 | `memory-import` памяти Claude из службы дал `files: 0` с кодом 0 | `filepath.Glob` молча глотает отказ в доступе к `/root/.claude` | 0.11.3: проверка `ReadDir`, пустой каталог — ошибка |
| MCP-169 | `mission_status` в проде всегда `unknown`, `incomplete=observation_limit` | больше 16 наблюдений (живые lease) считались неполнотой отчёта | 0.11.3: оценка до 256, показ 16 самых срочных, `omitted` |
| — | `release_apply action=queue` вставал на SHIP: «unknown tool "integration_ship"» | `integration_ship` зарегистрирован через `mcp.AddTool` без вызова по имени | 0.11.3: вызов по имени (`tool_invoke`, сторож автовыпуска) |
| — | «как управлять умными устройствами дома» не находило правило про Home Assistant | поиск только по словам: у записи другие слова | 0.11.3: вопросы-синонимы и смысловой слой |
| — | `mission_status` всегда `unknown/truncated` | любой идущий job на хосте делал отчёт неполным; освобождённые lease (>32 записей) обрезали чтение и считались писателями | 0.11.2: job занимает только свой проект, lease по `held`, чтение страницами, поле `incomplete` |
| — | импорт памяти Claude: R@3 0.44 | RRF k=60 и множители канона перебивали ранг | 0.11.2: k=10, канон ×1.1, описание в теле |
| BUG-20261109-1 | Zigbee2MQTT лежал ~1.5 ч: go test в консоли ИИ мимо лимитов отнял CPU, ASH-таймауты, containerd оставил task-dir | у дома не было приоритета над тяжёлым, MCPshka не видела здоровье Zigbee | 0.9.9: homeguard (веса, потолок user.slice, проба → очередь ждёт), правило п. 10; сторож `zigbee-usb-recover` поднимает застрявший Z2M |
| BUG-20261109-2 | CI Lane упал на 504 при загрузке Python, но status не сказал «сбой площадки» | шаблон знал «HTTP Error 504», GitVerse пишет «Unexpected HTTP response: 504» | 0.9.9 |
| — | ChatGPT останавливался на каждом CI и ждал владельца | у CI проекта был только разовый status «RUNNING, проверяй раз в 60 с»; правила 10/14/16, runners и tests расходились | 0.9.9: `forge_ci mode=wait` + согласованные правила |
| MCP-158 | CI Lane упал на загрузке Python (HTTP 504), повторить нельзя | для проектов forge_ci умел только publish/status; повторный publish того же коммита не создаёт прогон | 0.9.8: `mode=rerun` (workflow_dispatch) и `infra:true` в status |
| MCP-103 | preflight требовал план затронутых путей для изменения, действующего только после перезагрузки | не было понятия «следующая загрузка», пустые пути всегда «unknown» | 0.9.8: `effective=next_boot` + проверенный резерв |
| — | раннер GigaIDE не брал задания (NO_RUNNER) | агент ставил sqlite3/PyYAML только через sudo | 0.9.8: агент v3 качает их сам; `runner_task` называет недостающее сразу |
| MCP-144 | брошенный root-lease нельзя снять штатно: stale-проверка всегда находит живые процессы | прод запускался с cwd внутри репозитория, он сам и его sqlite3 считались держателями | 0.8.1: `WorkingDirectory=/root/repos`, тест-страж |
| MCP-126 | CLIENT_SCHEMA_STALE: старый список инструментов не обновлялся | отсутствовал явный HasTools для динамического добавления, stale-подсказка не содержала списка изменений | 0.7.3: list_changed и дельта имён/схем; полное переподключение клиента отдельно |
| MCP-127 | timeout 1944 с отвергался старым пределом 1800 | расходились потолки таймаутов | существующий фикс, регрессия 1800/1944/21600/21601 в 0.7.3 |
| MCP-128 | Git proof overflow на большом репозитории | малый бюджет доказательства | существующий фикс, бюджет 2 MiB и тест 3000 файлов в 0.7.3 |
| MCP-129 | после замены бинарников broker сохранил устаревшие лимиты PID1 | updater не сверял systemd drop-in | 0.7.3: root-owned resource drop-in с backup/rollback, независимый project_verify |
| MCP-131 | Face ID approval URL с длинным task id | ссылка не использовала разрешаемый уникальный префикс | 0.7.3: короткий префикс + защита коллизий |
| MCP-134 | неверный критерий phase_checkpoint_record оставлял PENDING | shape validation после idempotency Begin | 0.7.3: структурная проверка до Begin |
| MCP-136 | integration_ship: ключ воркера навсегда UNKNOWN после PROJECT_BUSY | PROJECT_BUSY был простым HOLD, квитанция оставалась PENDING | 0.7.3: PROJECT_BUSY — no-effect, ключ освобождается |
| MCP-135 | fs_patch PENDING/UNKNOWN после CONTEXT_DEADLINE (budget_ms=0) | обработчик стартовал с истёкшим ctx; PENDING без способа сверки | 0.7.3: проверка ctx до обработчика; CAS-доказательство по SHA файла снимает PENDING при повторе |
| MCP-132 | `phase_checkpoint_record`: «before probe failed; request held: path is empty», ключ навсегда UNKNOWN | проба `phase_gate` без аргументов; сбой before-пробы не освобождал ключ | 0.7.2: аргументы проб по схеме, проверка всех контрактов при старте, освобождение ключа |
| MCP-130 | `issue_update/accept/dispatch`: «before probe failed; request held: issue not found» | проба контракта `issue_get` вызывалась без `id` | 0.7.1: `ProbeArguments` передаёт `id` |
| MCP-125 | `integration executor recovery: context deadline exceeded` почти каждый проход брокера | 60 закрытых запусков перепроверялись каждые 30 с (retire + sqlite3) в общем бюджете 10 с | 0.7.0: память закрытых, бюджет на запись, health `recovery` |
| MCP-119 | `phase_gate(P26…P39)` PASS при «no checkpoint» | фаза без чекпоинта давала INFO, а не блокер; писателя чекпоинтов не было | 0.7.0: MISSING → BLOCKED, `phase_checkpoint_record` с доказательствами |
| MCP-121 | `fs_patch` при активном тесте: `FENCED_UNKNOWN / HOLD_REASON_UNCLASSIFIED` | нетипизированная строка «project job still pending: HOLD» | 0.7.0: `OWNER_BUSY/PROJECT_BUSY` + job_wait |
| MCP-122 | `timeout_seconds=7200`, но `cpu_time_limit_seconds=1800` | CPU масштабировался от запрошенного wall, а не от стандартного | 0.7.0 |
| MCP-123 | `service_logs(diagd.service)`: journal unit not allowlisted | журналы — только из broker.json, без health-юнитов | 0.7.0 + маскировка секретов |
| MCP-112 | Face ID: «no such file or directory» после подтверждения | повторный finish/options читал уже удалённый вызов | 0.7.0: ответ «authorized» по гранту |
| BUG-20261103-1 | `integration_publish_main` → «fast-forward outcome UNKNOWN» | неотслеживаемый файл в checkout на месте файла кандидата; HOLD без кода | 0.3.1 |
| BUG-20261103-2 | MosHub CI image: `unshare(CLONE_NEWUSER): Operation not permitted` | раннер GitVerse сам в user namespace: buildah падает и от root (0.3.1 не помог, run 1997867) | после 0.3.2: `assemble.sh` без RUN (crane) |
| BUG-20261103-3 | правка `moshub-ci-image.yml` не пересобирает образ | путь самого воркфлоу не входил в `paths`; dispatch с ref=ветка исполняет main | 0.3.2 |
| BUG-20261103-4 | каждый `roadmap_run` Lane с 01.11 — UNKNOWN «no roadmap phases» | sealed `roadmapscan` принимал только `## P0 — `, Lane пишет `## P0: ` | 0.3.1 |
| BUG-20261103-5 | `forge_ci` с ошибкой ввода «съедал» ключ: повтор → already used / UNKNOWN | слой мутаций оставлял квитанцию PENDING при любой ошибке обработчика | 0.3.2 (`failure.Rejected`/`RejectedAs` + `idempotency.Discard` в слое мутаций и в `idempotency.Run`) |
| BUG-20261103-6 | `project_verify` ok=false «working tree is dirty» на чистом проде | неотслеживаемые локальные docs считались грязью, хотя публикация их игнорирует | 0.3.2 |
| BUG-20261103-7 | `release_local` десятки вызовов COLLECTING без прогресса | образы читаются чанками 32 КиБ по 40 с на вызов; не сказано, что `release_apply` уже можно | 0.3.2 (прогресс + `next_action`) |
| BUG-20261103-8 | нативные тесты core129 падают на раннере | проверка резерва диска читала реальный tmpfs `/tmp` (1.9 ГБ) | 0.3.2 (заглушка statvfs + тест отказа) |
| BUG-20261103-9 | аренда Lane висит «busy, ждать держателя» после остановки прогона | держатель (исчезнувший workflow job / остановленный roadmap_run) не может отпустить, статус не называл путь | 0.3.2 (`orphaned` + stale path) |
| BUG-20261103-10 | после push в GitHub — root-файлы в `.git` сервисного репо | GitHub push/fetch шёл от root по SSH (у mcpshka нет ключа) | 0.3.2 (`forge_ci provider=github`) |
| BUG-20261103-11 | `knowledge_write` с временем в будущем «съедал» ключ: повтор → already used | ошибка валидации не помечалась как отказ без эффекта | 0.4.0 |
| BUG-20261103-12 | `project_test`/`project_run` BLOCKED: брошенные разрешения executor ждали владельца (аудит ChatGPT 03.11) | закрыть доказанно брошенное мог только Face ID-reconcile | 0.4.0 (самоочистка `executor-self-heal` раз в 15 мин) |
| BUG-20261103-13 | `host_triage`: `openvpn@mandatory` critical + совет `journal_read`, которому юнит недоступен | пропускались только inactive+disabled, masked — нет | 0.4.0 |
| BUG-20261103-14 | `tool_forge audit`: `no_contract` у `integration_ship` и `tool_invoke` | диспетчеры без своей мутации не отличались от инструментов без контракта | 0.4.0 |
| BUG-20261103-15 | сборка MosHub CI-образа: push в `registry-hub.mos.ru` → 401 (run 1999339) | секрет `MOSHUB_REGISTRY_TOKEN` в GitVerse от 23.10 устарел; токен H96 push разрешает | владелец обновил секрет; с 0.5.0 — кнопка «Записать в секреты CI GitVerse» на «Подключениях» |
| BUG-20261103-16 | `codex login` из «Подключений»: «Failed to find executable /root/.local/bin/codex» | брокер работает с ProtectHome, а systemd-run ищет бинарник в своём пространстве имён | 0.5.0 (запуск через `/usr/bin/env HOME=/root`) |
| BUG-20261103-17 | тесты MosHub: git exit 129 и нет модуля `yaml` | образ на bookworm: git 2.39 без `--no-lazy-fetch`, нет python3-yaml; пустой digest в артефакте сборки | 0.5.0 (образ на trixie + python3-yaml, digest проверяется) |
| BUG-20261103-18 | `project_test`/`run` DEGRADED «resources» при 15 ГБ свободного | прогноз «до резерва» по короткому окну замеров (минуты) давал ложную тревогу | 0.5.0 (для окна < 2 ч порог 12 ч, а не 72 ч) |
| BUG-20261103-19 | `roadmap_run_status` показывает старые остановленные прогоны как ждущие действия | более новый прогон того же проекта не отмечал предыдущие | 0.5.0 (`superseded_by`, next_action none) |
| BUG-20261103-20 | ChatGPT отказался вести DIAG без Codex/Claude | инструкции MCPshka требовали `roadmap_run`/агентов для разработки | 0.5.0 (разработку ведёт ИИ в чате; агенты — только по прямому указанию человека) |
| BUG-20261103-22 | DIAG P25: тест 1800 с упал по таймауту, а повтор с 3600/7200 — «invalid Workbench binding: HOLD» без запуска | лимит 1800 с жил отдельно в пяти слоях (executor, Workbench, root broker ×2, pipeline), схема обещала 21600; отказ — уже после записи | 0.5.0 (гибкий бюджет `internal/budget`, один потолок 6 ч) |
| BUG-20261103-23 | CI на GitVerse/MosHub/GitHub и зеркала запускал ИИ руками (`forge_ci`), `integration_ship` только ждал | у ship не было шага CI | 0.5.0 (ship сам публикует, раздаёт на MosHub/GitHub, ждёт и зеркалит main на 3 площадки) |
| BUG-20261103-24 | DIAG P25 на 0.5.0: бюджет 1944 с отвергнут до запуска, задание mw0sqrk1-5 повисло UNKNOWN | ещё три проверки `> 1800` (PrepareTreeTest, PrepareRecipeTest, AbandonRecipePreparation) — лимит был раскопирован числом, сверки слоёв не было | 0.5.1 (правка ChatGPT 7ffe322 + тест-сторож `TestNoNumericTimeoutCapsInJobChain`) |
| BUG-20261103-25 | полный тест MCPshka: «Git proof overflow» | проверка подмодулей для восстановления аренды читала `git ls-files` с лимитом 8 КиБ (7 байт на файл → ~1170 файлов; в MCPshka 1227) | 0.5.1 (один бюджет 2 МиБ на все Git-чтения восстановления, тест на 3000 файлов) |
| BUG-20261103-26 | DIAG: задание mw0sqrk1-5 UNKNOWN, job_status/job_cancel не помогают | отказ отмены мёртвой подготовки проглатывался, наружу — только «status unavailable» | 0.5.1 (причина отказа в ответе; сам отказ — BUG-24) |
| BUG-20261103-27 | журнал временного юнита MCPshka читается только через Face ID-задачу | `journal_read` пускал лишь явный allowlist | 0.5.1 (собственные юниты MCPshka читаются; Codex-вход исключён) |
| BUG-20261103-28 | LEASE_HEARTBEAT_STALE во время долгого теста, ручной heartbeat | аренда жила только на heartbeat ИИ, который ждал задание | 0.5.1 (токен держателя обновляет heartbeat, задание держателя продлевает аренду) |
| BUG-20261103-29 | Google Диск: «нужен адрес из строки браузера…» после входа | Safari на iPhone копирует адрес без `http://`, разбор требовал полный URL с хостом | 0.5.1 (code/state из любого вставленного текста) |
| BUG-20261103-30 | Codex вошёл («Logged in using ChatGPT»), карточка — «проверьте вход»; кнопки форм Mail.ru/WebDAV/S3 не по размеру | состояние агента не читалось; `flex-direction:column` растягивал поля по высоте | 0.5.1 |
| BUG-20261103-31 | Face ID-ссылка из Termius открывается «Некорректная ссылка» | терминал переносит длинную ссылку, телефон открывает только первую строку — ID сессии обрезан | 0.5.1 (короткая ссылка /approval/<8 символов>, закладка /approval/ открывает последнее ожидающее подтверждение) |
| BUG-20261103-32 | вливание с evidence=gitverse: «HOLD: incomplete GitVerse run listing» при зелёном CI | проверка листала 3 страницы по 50 и отказывала, если третья полная, даже когда нужный прогон уже найден; прогонов стало больше 150 | 0.5.1 (при порядке «новые сначала» найденный прогон достаточен) |
| BUG-20261103-33 | запечатанный тест модуля (evidence=module) всегда падал на `invariants`: нет docs/INVARIANTS.md | снимок теста несёт только исходники выпуска без docs/, а тест читал документ | 0.5.1 (без docs/ тест пропускается; в CI полный checkout — проверяется) |
| BUG-20261104-38 | integration_ship evidence=gitverse вечно «CI_RUNNING» при зелёном прогоне | статус forge_ci (Python) требовал полный листинг, а прогонов больше 150; правка 0.5.1 закрыла только проверку в Go | 0.6.0 |
| BUG-20261104-35 | под нагрузкой (load 12 при пороге 6) тяжёлые работы ждали бесконечно, а broker давал BROKER_CIRCUIT_OPEN | load1 считает ожидание диска и скачет; медленный ответ broker считался отказом и размыкал цепь | 0.6.0 |
| BUG-20261104-36 | вливание HOLD: голова main не совпадает с закреплённой | в main коммитили напрямую мимо integration_ship; причина не называлась | 0.6.0 |
| BUG-20261104-37 | повторное вливание того же коммита получало результат прошлого элемента | ключ идемпотентности шагов ship без ID элемента | 0.6.0 |
| BUG-20261103-21 | облако нельзя подключить без своего OAuth-приложения (client id/secret) | веб-подключение 0.4.0 работало только через приложение владельца | 0.5.0 (кнопка «Подключить» через встроенное приложение rclone) |
