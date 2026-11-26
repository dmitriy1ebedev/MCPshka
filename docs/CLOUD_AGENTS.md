# Облачные агенты: C1, шаг 1

Проверено на H96 24 октября 2026 года. Это отдельный путь от локального
`agent_task`: облачный агент не получает доступ к MCPshka, SSH или рабочему
дереву хоста. Результат всегда UNTRUSTED, `deploy_authorized=false`.

## Что проверено командами

| Проверка | Результат |
| --- | --- |
| `codex --version`, `codex --help` | codex-cli 0.164.0, присутствует экспериментальный `cloud` |
| `codex cloud --help` | `exec`, `status`, `list`, `diff`, `apply` |
| `codex cloud exec --help` | обязательный `--env`; есть `--branch`, `--attempts` (по умолчанию 1); prompt позиционный |
| `codex cloud list --help` | `--env`, `--json`, `--limit 1..20`, `--cursor` |
| `codex login status` | вход ChatGPT активен |
| `codex cloud list --json --limit 1` | exit 0, `tasks: []`, `cursor`; доступ к списку подтверждён, доступ к конкретному environment ещё не подтверждён |
| `claude --version`, `claude --help` | Claude Code 2.1.310; есть `--cloud`, `--environment`, `--teleport`, `--remote-control` |
| `claude auth status --json` | loggedIn=true, authMethod=claude.ai, subscriptionType=pro, apiProvider=firstParty; персональные поля не сохранялись |

В service PATH MCPshka этих CLI нет. `exec_start` с абсолютными путями
`/root/.local/bin/codex` и `/root/.local/bin/claude` завершился Permission denied.
Поэтому справка и безопасные проверки входа прочитаны из текущей root-сессии;
права каталогов, PATH службы и авторизация не менялись. Реальных облачных задач
при разработке не запускали, исходники никуда не публиковали.

Официальный Codex Cloud требует подключённого репозитория и опубликованного
environment. Его ID выбирает владелец в `codex cloud`; наличие логина не
доказывает доступ к нужному environment. В CLI проверен запуск:

```text
codex cloud exec --env ENV_ID --branch mcpshka-snapshots/SOURCE_SHA --attempts 1 -- PROMPT
```

Официальный Claude Code запускает облачную сессию командой
`claude --cloud "задача"`; старое `--remote` — устаревший вариант.
`--remote-control` управляет локальной сессией и для C1 не подходит.
Нужен вход claude.ai и разрешённые организацией cloud sessions; для push/PR
нужно подключение GitHub с правом записи. Pro на хосте подтверждён, GitHub
подключение облака и разрешение организации отдельной задачей не проверялись.

Claude может загрузить bundle локального репозитория со всей историей и
изменёнными tracked-файлами. Кроме того, если ветка отсутствует на GitHub,
он может начать с другого опубликованного коммита. Поэтому `claude_cloud`
в новом инструменте возвращает `blocked/claude_snapshot_transport_not_admitted`.
Это явное ограничение текущей реализации; локальный Claude вместо облака
никогда не запускается.

Лимиты: Codex запускается с одной попыткой; остаток квоты аккаунта CLI здесь
не доказывает, числовое значение неизвестно. Claude cloud расходует общий
лимит Claude/Claude Code, отдельной платы за VM документация не указывает.
Платные повышения лимитов автоматически не включаются.

Источники: [Codex CLI reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli),
[Codex Cloud setup](https://learn.chatgpt.com/docs/cloud),
[Claude Code cloud sessions](https://code.claude.com/docs/en/claude-code-on-the-web).
Командные флаги выше дополнительно проверены установленными версиями.
JSON статусов сверялся с [официальным исходным кодом CLI](https://github.com/openai/codex/blob/main/codex-rs/cloud-tasks-client/src/api.rs):
`pending`, `ready`, `applied`, `error`. `ready`/`applied` означают готовность
проверить результат; ниже слово completed описывает завершение работы,
а не буквальное значение CLI TaskStatus. Неизвестный статус — HOLD.

## Инструменты и квитанции

Обычный Face ID даёт режим `native_only`: дополнительные ИИ запрещены.
Только по прямой просьбе владельца задайте `admin_session_begin.agent_consent`
с `path=/root/repos/mcpshka` и `agents=["codex_cloud"]` (либо именем другого
разрешённого провайдера). Владелец увидит проект и список перед Face ID.
Брокер проверяет согласие до отправки; имя провайдера и квота его не заменяют.
Отмена/опрос прежних задач не требуют расширять список агентов.

`cloud_agent_task` принимает `admin_session_id`, `provider`, полный `commit`,
`prompt`, отдельную `acceptance`, `idempotency_key`. Репозиторий фиксирован:
`dmitriy1ebedev/mcpshka`; нельзя подставить произвольный путь, executable,
remote URL, environment или main. Prompt — до 2000 байт, acceptance — до 800;
весь JSON для брокера — до 4096 байт. Рабочие изменения в снимок не входят.

Face ID проверяется до создания квитанции и повторно root-брокером до
обращения к провайдеру. Брокер запускает только root-owned установленные
`/usr/local/libexec/mcpshka-cloud-agent.py` и `mcpshka-cloud-snapshot.py`;
скрипты из изменяемой ветки не исполняются с root. Оба файла устанавливает
`packaging/native/install-broker.sh` при обычном обновлении брокера.

Перед отправкой SQLite WAL атомарно сохраняет квитанцию `unknown` и SHA-256
полного запроса, включая владельца, prompt и приёмку. Текст инструкций,
токены и CLI stderr не сохраняются. Инструкции передаются CLI через stdin,
не через argv. Один owner/key соответствует одному ID `dispatch-…`.
Параллельный вызов, повтор после рестарта и потерянный ответ
не отправляют задачу повторно. Изменение запроса с тем же ключом — HOLD.
Журнал ограничен 64 задачами; список — 20 записями каждого вида.

Адаптер использует **тот же** `snapshot_commit` из
`packaging/gitverse/snapshot_immutable.py`: родительский список пуст,
дерево совпадает с source commit, timestamps и сообщение детерминированы.
В GitHub создаётся только `mcpshka-snapshots/<source SHA>`, проверяется readback;
существующий другой SHA — отказ, force-push отсутствует. Git object store
получает объект снимка, refs и рабочие файлы хоста не меняются.
GitHub token читается только root из существующего
`/root/.config/gh-mcpshka.token`; в облако и argv он не передаётся.
У токена должны быть права на чтение и создание ветки этого репозитория.

Состояния: `blocked`, `unknown`, `submitted`, `running`, `awaiting_pr`,
`failed`, `result`. `cloud_task_status id=…` и `cloud_task_list` читают SQLite
без сети. Новые квитанции находятся в поле `dispatch`/`dispatches`, старое
поле `receipt`/`tasks` совместимо с прежним журналом наблюдений.

`cloud_agent_poll` под той же Face ID сессией опрашивает провайдера и обновляет
квитанцию CAS. Для каждого нового опроса нужен новый `idempotency_key`.
Просматриваются максимум три страницы по 20 задач, environment и ID должны
совпасть. Отсутствующая задача, неверный источник или исчерпанный бюджет
не считаются успехом. Root-вызов ограничен 25 секундами; незавершённая
группа процессов завершается. Таймаут отправки остаётся UNKNOWN.

## Результат и обычная интеграция

`codex cloud exec` не гарантирует автоматический push/PR. После `completed`
владелец открывает задачу в Codex Cloud и публикует PR из
`cloud-agents/<dispatch ID>` в `mcpshka-snapshots/<source SHA>`, с ID облачной
задачи в body. До этого квитанция остаётся `awaiting_pr`. Адаптер не скачивает
и не применяет diff на H96. Это необходимый ручной шаг текущего CLI-маршрута.

При опросе проверяются GitHub API: оба конца PR в нашем репозитории,
точные head/base branch, SHA base равен неизменяемому снимку, PR открыт,
возвращены точный PR URL и полный SHA результата; compare API подтверждает,
что коммит результата происходит от снимка. В квитанцию `result`
попадают head branch, head commit, PR URL. Они остаются UNTRUSTED:
облачное `completed` и даже существующий PR не являются приёмкой.

Снимок parentless: облачную историю нельзя напрямую вливать в main.
При обычном review перенести дельту source snapshot → cloud result в
изолированную ветку от исходного source commit и сверить итоговое дерево
с облачным result tree. Новые инструменты этот перенос на хост не выполняют.

Далее используется обычный путь MCPshka: опубликовать **коммит подготовленной
ветки результата** через
`forge_ci provider=gitverse mode=publish`, дождаться PASS, запечатать
`integration_test_prepare evidence=gitverse` для того же коммита и передать
его в `integration_ship`. Новые инструменты не вызывают merge, deployment,
`integration_approve`, не создают разрешения и не подменяют CI-квитанцию.

UNKNOWN без provider task ID автоматически не восстанавливается по сходству
заголовков: владелец проверяет список облачных задач, прежде чем разрешать
новую попытку с новым ключом. Это предотвращает повторное списание квоты.

## Что осталось владельцу

На момент проверки файл `/var/lib/mcpshka/cloud-agents/config.json` отсутствует.
После обычной установки проверенной версии брокера выбрать в `codex cloud`
environment именно нашего GitHub репозитория и один раз выполнить:

```text
sudo /usr/local/libexec/mcpshka-cloud-agent.py configure ENV_ID
```

Команда создаёт root-only mapping без отправки задачи. Логины на хосте уже
есть; при их отзыве команды владельца — `codex login` и `claude auth login`.
Затем открыть Face ID через `admin_session_*` и выполнить небольшой
контрольный `cloud_agent_task`. В этом изменении такой прогон, установка
брокера и реальная цепочка PR → GitVerse → integration_ship не выполнялись.

Для Claude следующий шаг — отдельно реализовать и проверить транспорт
единственного снимка и безопасный опрос облачной сессии; одних локальных
`--help` и активного claude.ai login для допуска недостаточно.

## Локальная приёмка

24 октября 2026: `project_build` — PASS; `go vet -p 1` для
`internal/cloudtasks`, `internal/mcpserver`, `internal/rootbroker` — PASS.
`project_test`: весь пакет `internal/cloudtasks` — PASS; для mcpserver и
rootbroker фильтр `Test(Cloud|AllMutationContracts|Capability|AdminSession)` —
PASS, включая офлайн Python-контракты адаптера. Проверены SQLite restart,
конкурентный единственный submitter, конфликт prompt/приёмки, UNKNOWN без
повторной отправки, CAS, отказ зарегистрированного инструмента без Face ID,
границы PR и ограничение вывода. `bash -n packaging/native/install-broker.sh`
и `git diff --check` — PASS. Полный `go test ./...` оставлен CI.
