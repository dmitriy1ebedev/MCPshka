# approved_task: порядок работы оператора

Один закреплённый root-план = один Face ID владельца. Админ-сессия (`admin_session_*`) его не заменяет: `admin_coverage` отвечает `needs: plan_face_id`.

## 1. Подготовка

`approved_task_prepare` (или `webos_deploy`) с `idempotency_key`. Сохраните ключ: он и есть способ продления.

Необязательные поля плана (все закрепляются в plan_hash и видны на странице Face ID):

- `units` — до 4 `.service`, которые план ставит (не `mcpshka*`). После запуска они читаются `service_status`/`service_list` (`owned_by: approved_task:<id>`), только статус.
- `probe` — `http://<loopback|частный IPv4>:<порт>/путь`, проверяется с хоста после запуска.
- `reach` — до 4 `ip:порт`, которые должны отвечать ДО расхода согласия (webos_deploy ставит `<tv>:9922` сам).

### Пакет из project_pipeline

В `inputs` и отдельных элементах `args` можно передать `pipeline:out/app.bin`.
Путь относится к корню репозитория `workdir`: сервер находит проверенный
экспорт package текущего HEAD и передаёт брокеру абсолютный путь и SHA-256.
Аргумент автоматически становится закреплённым input. Подмена файла до
подготовки или выполнения отклоняется. Произвольный путь хранилища не нужен.

Для скачивания: `artifact_get repo=/root/repos/project path=out/app.bin`.
Ticket привязан к пользователю; при скачивании снова проверяются HEAD,
манифест и SHA-256. Остальные файлы хранилища недоступны.

В argv рецепта `.mcpshka/release.json` используйте `{artifact:out/app.bin}`
(можно внутри аргумента, например `--package={artifact:out/app.bin}`).
Экспорт должен принадлежать выпускаемому коммиту. Подстановка идёт до
captured values: вывод предыдущего шага не может добавить ссылку на артефакт.

Нет проверенного пакета — выполнить указанный в ошибке
`project_pipeline path=<repo> stages=[package]`, затем повторить потребителя
с тем же относительным путём.

### Явный даунгрейд

Общий план принимает `allow_downgrade=true` и точную `target_version`
(например `0.1.0`). Оба значения закрепляются в Face ID; процесс получает
`MCPSHKA_ALLOW_DOWNGRADE=0.1.0`. Без флага значение пустое. Установщик
сам проверяет совпадение этой версии с пакетом. `webos_deploy` получает
версию из IPK и сохраняет совместимость с `MCPSHKA_WEBOS_ALLOW_DOWNGRADE`.

## 2. Согласие

`approved_task_status` → `approval_state`:

| approval_state | Что делать |
|---|---|
| `awaiting_face_id` | дать владельцу `approval_url`; страница сама обновляет challenge до `approval_deadline` (1 ч) |
| `authorized` | `approved_task_execute` до `approval_deadline` (10 мин после Face ID) |
| `approval_expired` / `authorization_expired` | `approved_task_prepare` с ТЕМ ЖЕ ключом и тем же планом → тот же id и URL, новый challenge; снова Face ID |
| `consumed_unrecorded` | UNKNOWN: ничего не повторять, сверка |

Изменился план или файлы — тот же ключ даёт HOLD; нужен новый ключ и новый Face ID.

## 3. Запуск

`approved_task_execute id=…`. Недоступный `reach` → `HOLD preflight … approval was NOT spent`: включить устройство и повторить тот же execute (после 10 мин — продлить, п. 2).

## 4. Итог

`approved_task_status` → `effect`, `verify[]`, `next_safe_action`:

- `applied` — DONE.
- `none` — ничего не запускалось (сработала проверка пинов); исправить причину.
- `unknown` — запускалось и упало/пропало: **не выполнять снова**; проверить `units`/`probe`, `service_status`.
- `observed: declared_state_present` при `unknown` — хост видит объявленные units/probe здоровыми. Это наблюдение, не доказательство (unit мог работать и до запуска): не повторять, подтвердить результат (версия, содержимое, unit-файл) и только тогда считать применённым.

## Чего MCPshka не контролирует

Блок вызова tool-safety клиента (ChatGPT) — вне сервера; продление тем же ключом не требует нового вызова с новым ключом. Выключенный ТВ / истёкший Developer Mode — внешнее состояние: preflight лишь не даёт потратить согласие.
