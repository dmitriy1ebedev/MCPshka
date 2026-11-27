# MCPshka 0.16.5

Код подготовлен в claude/0165b от production 0.16.4. Выпуск выполняет чат;
production в этой задаче не обновляется.

- Автовыпуск без раннера GitVerse использует GitHub. После ручного вливания
  очередь выпуска продолжает существующий ship вместо повторного эффекта.
- Distributed CI сохраняет успешные и активные шарды, перераздаёт офлайн
  очереди живым площадкам и использует pull-раннер для оставшихся шардов MCPshka.
  CODE_FAIL не скрывается повтором. GitVerse waiting/cancelled получают
  CI_NO_RUNNER/CI_PLATFORM_CANCELLED; отсутствие подтверждения квоты остаётся
  неизвестным, а не выдаётся за исчерпание.
- GigaIDE просыпается только для ожидающего задания с известным бюджетом vCPU.
  Резервирование пробуждения и часов сохраняется между процессами; повторный
  postStartCommand не запускает второго агента. runner_status и MOTD показывают
  живость и последнюю задачу.
- fixes_night: все открытые P1–P3 MCPshka/Lane/DIAG, P1 и повторяющиеся
  сбои первыми. План → патчи по шагам в отдельных fix/<card> worktree,
  commit и project_test. Ночной серверный цикл запускает конвейер при тихом
  H96. Секреты не отправляются, хост не меняется. Только утренний
  fixes_review переносит зелёные ветки в чистый worktree того же проекта;
  автоматического вливания и выпуска нет. Потерянный ответ теста
  восстанавливается по исходному ключу.
- При BROKER_CIRCUIT_OPEN/преддиспетчерском BUSY agent_task убирает только
  чистую собственную подготовку и разрешает повтор тем же ключом.
  Неизвестный ответ и неудачная очистка сохраняют fencing.
- MCP-209: project_artifact_cleanup preview/apply работает по явному списку
  под lease, сверяет inode/owner/bytes/SHA-256. Живые задания, tracked файлы,
  symlink, production и rollback блокируют удаление.
- MCP-228: project_test local=true обходит external_only и идёт на H96;
  distributed при отсутствии площадок возвращает NO_LIVE_CI_PROVIDER,
  список недоступных провайдеров и next_safe_action. Установка remote
  артефактов сохраняет проверку SHA-256.

- lane: тонкая обёртка в internal/tools/lanetools. Чтение status, engines status,
  router status/wifi/clients/history и device list через root broker без Face ID.
  Изменения требуют Face ID: router plan → apply с before_sha256 в stdin JSON,
  engines update --apply, dns apply/commit, service mode (штатный dpi mode Lane).
  Рецепты фиксированы, произвольные CLI-аргументы не принимаются; JSON Lane
  передаётся как есть. Один сеанс роутера, код BE900_SESSION_BUSY.
- resume_of автоматически снимает lease завершённой agent_task через прежний
  broker stale-preview/release под Face ID, без 30 минут ожидания: точный
  владелец, запись завершённой попытки, наблюдаемые inactive/failed runner
  и attempt units, отсутствие jobs и процессов. Ошибка наблюдения сохраняет HOLD.

Живые пробуждения облаков, полная четырёхплощадочная приёмка, расследование
квоты GitVerse через UI, ночные внешние LLM-отправки и выпуск требуют
отдельной приёмки. Эта задача не меняет Lane, DIAG, сеть и службы хоста.

Живой запуск GigaIDE блокируют отсутствующие в действующем окружении
MCPSHKA_GIGAIDE_WORKSPACE_URL и подтверждённый
MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING (проверены только признаки наличия,
значения и секреты не выводились).

GitVerse: у проверенных waiting/cancelled jobs runner_id=0, старт не отмечен,
шагов нет — выполнение кода не начиналось. Причина отказа площадки и квота
не подтверждены; CODE_FAIL этим наблюдением не подменяется.

Проверка кода — project_test PASS: agents, projectlease, brokerclient,
distributedci, forgeci, gitprovider, jobs, offbox, runnerci, runnerhub,
artifactcleanup, freellm, gitops, toolcontract, providers, rootbroker,
tools/orchestration, tools/lanetools, archlint и cmd/mcpshka.
Целевые mcpserver: snapshot, полнота схем/digest, external_only + local:true,
cleanup при живом job, возврат lease и загрузка артефакта — PASS.
Пакеты tools/projectops, tools/fixes, tools/issues и tools/capabilities
скомпилированы; собственных test-файлов в них нет.
Archlint: mcpserver 9976/9998, rootbroker 11995/11995; лимиты не увеличены.
