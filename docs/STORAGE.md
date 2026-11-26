# Storage Fabric: первый срез S4 и локальная основа S4.K

## Границы

SQLite остаётся истиной knowledge. Облако — недоверенный транспорт.
Коннекторы чата не передают MCPshka свои полномочия.

Реализован один контракт `storagefabric.StorageProvider.Do` с адаптером Engine
для фиксированного `/usr/local/bin/rclone` (целевой установленный 1.75).
Провайдеры: `drive`, `yandex`, `dropbox`; `iclouddrive` объявлен
`UNSUPPORTED`. Его web-сессия Apple ID/2FA не удовлетворяет требованию ROADMAP
об официально поддерживаемом серверном доступе. Apple cookies не принимаются.

## OAuth: действие владельца

Подключение — по ссылке `storage_connect provider=drive|yandex|dropbox`: страница ведёт по шагам,
ключи и токены вводятся в браузере и в чат не попадают (`docs/STORAGE_CONNECT.md`).
Прежний приём JSON через `storage_auth_accept` скрыт из каталога (вызывается по имени).

Для iCloud команды OAuth нет: `rclone authorize "iclouddrive"` не заменяет
Apple ID/2FA. Нужен отдельный одобренный официальный bridge; этот срез не
предлагает извлекать cookies или пароль Apple ID.

`storage_providers` / `storage_auth_status` различают отсутствие credential,
его наличие и неподдерживаемый backend. Наличие credential = `unverified`.
Проверять доступ после авторизации: создать корневую папку
`storage_mkdir(provider, path="")`, дождаться `storage_transfer_status(id)`,
затем `storage_list(provider, path="")`. Реальный успешный list подтверждает
доступ на момент вызова, но не бессрочность токена и не backup quorum.

## Vault и изоляция

Конфиги и refresh tokens: `/var/lib/mcpshka-vault/storage/<provider>.conf`.
Ключ backup: `/var/lib/mcpshka-vault/storage/backup.key`.
Каталоги root:root 0700, файлы 0600; vault находится вне writable state сервиса.
Broker принимает строго типизированный JSON по Unix socket.
JSON с OAuth передаётся worker через stdin, не argv. Ответы не содержат config
или token. MCP audit/receipts сохраняют fingerprint и безопасные результаты.
stdout/stderr rclone не попадают в журнал; stderr worker выключен.

Worker выполняется отдельным root systemd unit: CPUQuota=50%, MemoryMax=256M,
TasksMax=24, RuntimeMaxSec=1800, UMask=0077, NoNewPrivileges,
ProtectSystem=strict, ProtectHome=read-only, PrivateTmp/PrivateDevices,
ограниченные writable paths и capabilities.
rclone: один transfer/checker, buffer 1 MiB, без multi-thread,
8 MiB/s, одна попытка, ограниченные timeouts и metadata output.
Обновление credential сериализовано по провайдеру. Один storage job за раз.

## Инструменты и пути

- Чтение: `storage_providers`, `storage_auth_status`, `storage_list`,
  `storage_stat`, `storage_transfer_status`.
- Изменения: `storage_mkdir`, `storage_upload`, `storage_download`,
  `storage_move` (и переименование), `storage_trash`.
- Backup: `backup_plan`, `backup_create`, `backup_status`, `backup_verify`,
  `backup_restore_plan`.

`provider` обязателен для облачной операции. `path` и `destination`
относительны к отдельной папке `MCPshka` в выбранном облаке.
Абсолютные пути, traversal, rclone remote/config syntax, служебные
`.trash` / `backups` отклоняются. Выход за папку не реализован:
он потребует отдельного scoped approval в будущем срезе.

`storage_upload` принимает только `artifact_id` завершённой загрузки
текущего MCP principal. Размер до 256 MiB (нынешний inbox ограничивает его
дополнительно до 16 MiB). `storage_download` пишет только в root-only
`/var/lib/mcpshka-storage/staging/<operation-id>.blob`, возвращает путь,
размер и SHA-256 после завершения. Байты не передаются через JSON.
Для upload worker открывает все компоненты пути без symlink, отклоняет hardlink,
создаёт приватный staging-снимок и сверяет SHA-256/размер готового artifact
перед отправкой. Подмена содержимого отклоняется без cloud write.

mkdir/upload/download/move/rename/trash возвращают durable operation ID.
Опрос: `storage_transfer_status(id)`. Состояния pending/running/done/failed/UNKNOWN.
Повтор исходного запроса — с тем же retry key; другой payload с тем же ключом
отклоняется. При UNKNOWN сначала сверить удалённый объект и unit, не создавать
новый ключ вслепую. Автоматический replay неизвестного эффекта отсутствует.
Полученный ID принадлежит исходному principal. Статус не отменяет операцию.

Перезапись запрещена через immutable; `trash` перемещает объект в
`MCPshka/.trash/<operation-id>/<name>`. Это прикладная корзина, доступная
для ручного восстановления, а не необратимое удаление и не provider purge.
API необратимого удаления и share в этом срезе отсутствует.

List ограничен одним уровнем и 60 KiB вывода: переполнение возвращает ошибку,
а не молча обрезанный список. Pagination/resume/cancel — следующий срез.

## Backup репозиториев

`backup_plan(repositories=["/root/repos/project", ...])` проверяет прямые,
несимлинковые Git-каталоги. До 16 репозиториев за один ограниченный снимок.
`backup_create` принимает тот же список, `provider`, `idempotency_key`.
Используется `git bundle create - --all`: refs и committed objects, без
working tree, untracked, окружения процесса и credential helper.
Manifest отмечает исключённые рабочие изменения; bundle HEAD сверяется с plan.
Секреты, когда-либо закоммиченные в Git, могут присутствовать внутри bundle:
они остаются под шифрованием; этот инструмент не переписывает историю.

Manifest v1 содержит дату, имя/путь репозитория, HEAD, размер и SHA-256 bundle.
Он упакован вместе с bundle в tar и зашифрован **до upload**.
Облачное имя непрозрачное: `MCPshka/backups/<operation-id>.enc`.
Локальный ciphertext хранится в `/var/lib/mcpshka-storage/backups`.

Шифрование — эквивалент age по требованию данного среза: стандартный AES-256-GCM,
64 KiB фреймы, случайная 32-byte соль для каждого backup, HMAC-SHA256 derivation
от локального 32-byte master key. Header является AAD, nonce — номер фрейма.
Обязательный аутентифицированный пустой фрейм завершает поток:
обрезание, перестановка, подмена и лишний хвост не принимаются.
Формат: ASCII `MCPS4ENC1`, 32 bytes salt, затем повторяющиеся
uint32 big-endian ciphertext length + GCM ciphertext/tag.
Ключ не отправляется в облако. Сохраните его отдельную offline-копию;
потеря ключа делает восстановление невозможным.

До 256 MiB на снимок, пиковые временные данные до 768 MiB, запас диска —
max(3 GiB, 10% раздела). Собственные plaintext bundle удаляются после job.
Большие репозитории делите на отдельные backup sets; автоматическое
превышение бюджета не допускается. Retention и массовая очистка не включены.

Upload не равен verification. Вызовите:
`backup_verify(destination="<backup-id>", idempotency_key="...")`,
затем `backup_status(id="<verify-operation-id>")`.
Проверка скачивает ciphertext в новый файл, сравнивает SHA-256/размер,
проверяет AEAD и завершение потока, manifest и хэши всех bundle.
После этого исходный backup получает `verified`.
Проверочная копия удаляется после сверки. Нужен исходный локальный ключ. UNKNOWN после отправки backup можно
сверить через backup_verify; новая отправка не запускается.

`backup_restore_plan(id="<verified-backup-id>")` выдаёт проверенный manifest, путь нового staging
и порядок проверок. Он не восстанавливает файлы и не пишет в production.
Независимый recovery читает описанный формат через Encrypt/Decrypt и
DecodeManifest, затем запускает `git bundle verify` и клонирует в новый staging.
Исполнение restore, offline recovery CLI и restore drill — следующий срез.

## Knowledge: ревизии и экспорт чатов

`knowledge_write`:
- add / import — существующий контракт;
- update / merge / supersede / archive / delete — `id`, `reason`,
  `expected_revision`; update/merge требуют replacement `record`,
  merge — `sources`, supersede — новую запись с другим ID;
- forget — прежнее полное стирание с зависимостями для совместимости.
  Для обычного удаления используйте delete: tombstone с историей.

Правки и изменения связанных записей фиксируются одной SQLite CAS.
Stale revision отклоняется. Старые записи получают базовую revision=1
при чтении. История содержит время, причину, операцию и предыдущий снимок.
archive/delete/superseded не участвуют в активном поиске.
Внешние projections не содержат историю.

`knowledge_local_read(id, history=true)` — отдельное разрешение для владельца:
читает приватные импортированные статьи и историю с маскированием секретов.
Параметр `revision` выбирает один прежний снимок; ответ ограничен 64 KiB.
История входит в общий бюджет SQLite 4 MiB, отдельно от лимита текущей статьи.
Не передавайте его внешним AI provider как portable projection.
`knowledge_read/search` сохраняют прежнюю экспортную границу.

`knowledge_import_chat(artifact_id, format, idempotency_key)`:
форматы `chatgpt`, `claude`, `markdown`, `chatgpt_zip`, `claude_zip`.
ZIP читается без распаковки на диск; используется единственный conversations.json.
Пределы: 16 MiB входа/распакованного JSON, до 1000 ZIP members,
до 2000 excerpts, 32 KiB текста excerpt, SQLite до 4 MiB.
JSON декодируется по conversations; Markdown — по строкам/ограниченным chunks.

Исходный ready artifact остаётся неизменным. Provenance: artifact ID и SHA-256;
дата импорта сохраняется в записи. IDs — хэш маскированного текста,
повторный импорт дедуплицируется. Пароли/token assignments, bearer,
типовые API tokens, JWT и private-key blocks маскируются.
Все импортированные поля приватные и не выгружаются без явного export opt-in.
Маскирование эвристическое: проверка владельцем перед публикацией обязательна.

Это нормализованные excerpts, а не автоматически подтверждённые факты.
Извлечение решений моделью, original-message timestamps/attachments, HTML,
semantic search, split и двусторонний Google Docs sync — следующие срезы.
Для публикации статей введён интерфейс ArticlePublisher; реализации Google Drive
и передачи секретов внешнему провайдеру в этом срезе нет.

## Приёмка и ограничения

Локально: build/vet, project_test затронутых пакетов и regression tests.
Полный suite — только CI. Реальный OAuth, cloud upload/readback, два независимых
провайдера и restore drill проверяются владельцем после авторизации и установки.
Полную фазу S4/S4.K эти локальные проверки не закрывают.
