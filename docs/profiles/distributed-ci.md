# DIAG и Lane: сборка вне H96

В MCPshka 0.16.4 DIAG и Lane имеют встроенную политику профиля:
`"ci":{"provider":"distributed","external_only":true}`. Локальный профиль
может задавать команды проекта; встроенная политика размещения сохраняется.
`project_test` и `project_build` запускают один `forge_ci` job режима `wait`
для чистого committed HEAD. `project_run` и `project_pipeline` не запускают
локальную сборку этих проектов и указывают тот же внешний маршрут.

Явный маршрут: `forge_ci provider=distributed mode=publish|status|wait
path=/root/repos/diag commit=<полный SHA>` (или `/root/repos/lane`).
План — `mode=plan`. Четыре первичных шарда: GitHub, GitVerse, MosHub,
SourceCraft; при недоступности площадки/INFRA_FAIL шард принимает следующая.
Ошибка кода останавливает прогон. Для этих профилей H96 не принимает
недостающие шарды: итог INFRA_FAIL требует восстановления внешней площадки.

В каждом приватном репозитории площадки нужны CI-файлы из
`packaging/ci/project-ci/` и `.ci/distributed.json` с ARM64-целями:

```json
{"builds":[{"package":"./cmd/diag","path":"out/distributed/diag"},
           {"package":"./cmd/diagd","path":"out/distributed/diagd"}]}
```

У Lane цель `./cmd/lane-go`, файл `out/distributed/lane-go`. Дополнительные
Python/Make-проверки задаются `checks:[{shard:1,argv:["python3",...]}]`;
они исполняются только непривилегированным CI worker. `.ci/mcpshka-revision`
закрепляет полный SHA проверенного MCPshka worker. Этот commit должен быть
доступен на GitHub до запуска проектного CI. На SourceCraft конфигурация
триггера также должна находиться в default branch. Публикация кодового
релиза MCPshka и настройка удалённых приватных репозиториев — отдельный шаг.

Shard 0 после тестов собирает готовые Linux ARM64 файлы. Отчёт привязан к
source commit, tree, snapshot и plan hash и содержит размер/SHA-256 каждого
файла. MCPshka скачивает артефакты с площадки в ограниченный приватный store,
проверяет размер и SHA-256. Рецепт установки на H96 использует
`{artifact:out/distributed/diag}`: перед установкой файл хешируется повторно.
Для Lane аналогично `{artifact:out/distributed/lane-go}`. Непроверенный файл,
другой commit или несовпадение SHA останавливают установку.

diag_release_apply также берёт пару diag/diagd из проверенного CI-store и
сохраняет smoke, swap и rollback; локального go build в нём больше нет.
project_release отклоняет прежний рецепт с локальным компилятором или
интерпретатором. 0.16.4 не меняет рецепты в репозиториях DIAG/Lane: переход рецепта
на CI-артефакты подлежит существующей проверке и привязанному approval.
Никаких изменений маршрутов, xray, tun или действующих служб этот CI-путь
не выполняет.
