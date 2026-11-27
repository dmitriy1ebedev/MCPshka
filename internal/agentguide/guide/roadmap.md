# Задача и ROADMAP

## Пункт roadmap — это чекбокс (0.11.6)

«Делай пункты roadmap» = закрывай чекбоксы. Цикл одного пункта:

1. `roadmap_next path=…` — наряд: `item.id`, текст, `item.exit` (когда пункт сделан), `phase_rules` (обязательные тесты и правила фазы), `stages`, `also_open`. `warning` говорит, если у пункта нет Exit (сначала допиши его в строку пункта) или если коммиты копятся, а ROADMAP не меняется.
2. code → tests → ci: реализуй пункт целиком, коммиты «<ID>: …», точечные тесты, `forge_ci`, `phase_gate` = CODE_TEST_PASS.
3. install: если Exit говорит о работающей команде, службе или проде — выпусти и установи. Код в репозитории не равен установленному.
4. accept: каждое условие Exit проверь заданием MCPshka (`exec_start`, `project_run`, `project_verify`) и запиши `phase_checkpoint_record phase=<ID>` с `evidence job:<id>`.
5. close: `roadmap_close items=[<ID>]` под своим lease — отметит `[x]`, дату и твою заметку, закоммитит ROADMAP и вернёт следующий наряд. Отказ ничего не меняет и называет недостающий шаг.

Пачка: этапы 2 для всех пунктов, один CI и один выпуск, затем 4–5 по каждому (`roadmap_close` принимает до 20 пунктов одним коммитом). Отчёт владельцу — в закрытых пунктах («было 0/6 → стало 2/6: GO0, GO1»), не в коммитах. Урок Lane G0 (11.11.2026): 60 коммитов «GO0…GO5», закрыто 0 из 6 — писались изолированные заготовки, ни один Exit не доведён до установки и сверки.

## Общий порядок

1. project_context + актуальный checkpoint/ROADMAP: цель, source, разрешённый scope, jobs, deploy, lease.
2. Прими задачу в текущем чате. mission_start сохраняет её независимо от чата; для установленного и принятого выпуска используй mode=REPAIR_AND_RELEASE и явно перечисленные полные gates. agents_allowed по умолчанию false.
3. Обнаружь точные схемы через capabilities_bootstrap/tool_resolve. Проверь capability_preflight, backend и разрешения; не выводи готовность задачи из одного healthy хоста.
4. Исполняй существующими native tools. Однократный SHA-pinned CAS в lease-free scope не требует лишнего lease. Длительный writer/commit — собственный lease и heartbeat; сохранить ключ и token приватно.
5. Короткие целевые RED/GREEN → review diff → project_commit. project_test/job_wait до terminal; при FAIL — bounded output и устранение доказанной причины. Интерпретатор, запрещённый exec_start, не обходить: профильный sealed workflow или CI с отдельным admission.
6. phase_checkpoint_record связывает критерии с реальными evidence/job и source; phase_gate даёт вердикт фазы. Пройденный общий тест не принимает фазу, build не принимает релиз.
7. Продолжай execute → verify → integrate → release → production acceptance → cleanup через существующие workflow_* и release tools. Pinned worker и completion outbox продолжаются после обрыва; UNKNOWN сначала reconcile исходных IDs/keys.
8. mission_tick/mission_resume наблюдают прежнюю миссию и продвигают доказанный checkpoint prefix. События, повторные и перекрывающиеся ticks не разрешают повтор эффектов. Реальный внешний scheduler может вызывать ticks до неизменного deadline; MCPshka не создаёт фонового мышления ChatGPT.
9. В finally: сохранить состояние; дождаться/сверить собственные jobs/deploy; освободить только свой idle lease либо штатно передать текущий holder. Чужой lease сохраняет fencing; UNKNOWN и активный writer не отнимать.
10. DONE — только после всех критериев порученного результата, реальной установки где она нужна, проверки production и отката, cleanup. Deferred долгие soak/chaos не обозначать PASS.

Дополнительный агент — только по прямому поручению владельца. agent_task выбирает ровно разрешённого исполнителя. roadmap_run требует allowed_agents=["codex"] или ["claude"]; оба допустимы лишь когда владелец разрешил оба. Ни общий запрос «развивай проект», ни quota не дают такого разрешения.

## Единый шаблон ROADMAP (0.11.3)

Один шаблон для всех проектов (MCPshka, Lane, DIAG и прочих): разделы «Релизы» (таблица «| Версия | Дата | Статус | Что сделано | Пункты |», версии просто 0.N.M, статус начинается с «в проде», «заменён», «план», «в сборке», «откат» или «отозван»), «Очередь» (пункт выпуска: версия, название, дата, слова владельца, приёмка) и «Реестр багов» (карточки issue_list, BUGLOG.md — журнал выпусков). Фазы проекта остаются своими. Методология одна — этот guide и topic=rules; AGENTS.md проекта только ссылается на них. Термины пишутся одинаково: MCPshka, Lane, DIAG, Face ID, GitVerse, Codex, ChatGPT, GitHub (цитаты владельца в «», код и ссылки не правятся). Отклонения показывают `project_context.roadmap_style` и проверка `roadmap_style` в `phase_gate` (WARN, не блокирует); орфографию отслеживаемых документов MCPshka проверяет CI.
