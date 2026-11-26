# project_pipeline

`project_pipeline` выполняет `prepare → build → test → lint → package → verify`
по проверенному профилю `project_profile`. Источник профиля остаётся прежним:
`.mcpshka/profile.json`, зарегистрированный профиль, встроенный профиль.
Произвольные команды в запросе отсутствуют. Make, Python, Go, Node и webOS
используют один механизм workflow; отдельного привилегированного host Make нет.

```json
{"path":"/root/repos/project","stages":["all"],"idempotency_key":"pipeline-1"}
```

Можно передать упорядоченное подмножество, например `["build","test"]`.
Все запрошенные workflow проверяются до первого запуска; отсутствующие стадии
не пропускаются. За вызов запускается максимум одна стадия. `state`,
`next_stage`, `job` и `evidence` показывают прогресс. Дождитесь `job_wait`, затем
повторите запрос с **новым** ключом для следующего шага. Повтор старого ключа
возвращает исходный ответ. Владелец interactive lease передаёт `lease_token`;
токен не сохраняется в evidence.

Нужны чистый закоммиченный HEAD, SQLite authority и Workbench executor.
Команда профиля, HEAD и digest профиля повторно сверяются при admission.
Каждая стадия получает свежий снимок HEAD: read-only исходник и его рабочую
копию в disk scratch внутри существующего offline sandbox. Workflow должен
быть самодостаточным: `package: make build` сам создаёт файлы для упаковки;
он не зависит от оставленного после `build` рабочего каталога. Зависимости
Python/Node готовит существующий механизм environments. Runtime binds остаются
allowlisted/read-only. Heavy queue и ограничения executor сохраняются:
до 1800 секунд, до 12 элементов argv, ограниченный вывод и ресурсы.

PASS evidence адресуется по canonical project path, HEAD, tree и SHA-256 всего
профиля: команды, env, runtime paths, лимиты и конфигурация артефактов.
При совпадении входов стадия возвращает прежний job/evidence с `reused: true`.
Новый HEAD (даже с прежним tree), tree или конфигурация создают другую запись.
Неуспешная стадия останавливает последовательность. `STARTING` без job после
обрыва — `UNKNOWN`, требующий сверки jobs; автоматического повторного запуска
при неизвестном исходе нет. Стадии build/package/verify не засчитываются
вместо sealed test в `phase_gate`.

Workflow `package` обязан объявить `artifacts`: до 16 точных относительных путей
к файлам, без glob, symlink, hardlink и выхода из проекта. До 64 МиБ на файл,
256 МиБ на пакет. Worker копирует их из sandbox в root-owned sealed archive
перед очисткой scratch. Broker отдаёт receipt и блоки до 32 КиБ только после
успешного результата и fencing permit. Controller повторно сверяет размер и
SHA-256 каждого файла и записывает их с исходными именами в
`<state>/pipeline/<identity>/`, рядом с `manifest.json`. Manifest содержит
проект, HEAD, tree, digest профиля/конфига, команду стадии, время, job/permit,
пути, размеры и SHA-256. Перед reuse файловое evidence перепроверяется.

Ретеншн — семь дней, очистка при последующих обращениях; удаляются только
собственные завершённые export/receipt с проверенной идентичностью, root archive
также требует fenced permit. Каждое хранилище ограничено 512 МиБ и отказывает
при нехватке места; export сохраняет свободными 3 ГиБ или 10% раздела.
Исчезнувшее после ретеншна package evidence возвращает `EXPIRED`; следующий
вызов заново запускает package. Повреждение файла остаётся HOLD.

## Профили приёмки

Примеры: [Free My TV](profiles/free-my-tv.json) и [DIAG](profiles/diag.json).
Это проверяемые входные профили; их применение к другим репозиториям требует
обычного `project_onboard` либо обновления локального профиля владельцем проекта.
Repository profile имеет приоритет над зарегистрированным.

Free My TV упаковывает `FreeMyTV-webos.ipk` и `FreeMyTV-overlay-webos.ipk`.
DIAG собирает `build/diag-linux-arm64` и `build/diag-linux-amd64`, выполняет
тесты/vet и native/matrix smoke. Docker smoke запускается отдельно существующим
`docker_smoke` с тем же HEAD, `packaging/docker/Dockerfile` и ограниченными
сценариями из проекта. Docker daemon/socket внутрь pipeline не передаётся.

Controller и broker должны поддерживать stage extras и pipeline receipt/read
одновременно. Вызов через старый установленный broker не является приёмкой
нового pipeline. Релиз и production deploy выполняются отдельным процессом.
