# MCPshka 0.1.184 — облачные хранилища, зашифрованный бэкап, редактируемая база знаний

- S4 (Codex): `storage_*` поверх rclone — Google Drive, Яндекс.Диск, Dropbox (iCloud — HOLD: rclone требует Apple ID и 2FA). Токены — только в root-only vault, rclone в отдельном ограниченном юните, запись только в папку «MCPshka» каждого облака, удаление — в корзину. Авторизация облака — владелец на компьютере с браузером: `rclone authorize "drive"` (и `yandex`, `dropbox`), результат принимает `storage_auth_accept`.
- Бэкап репозиториев: git bundle + manifest SHA-256, шифрование до отправки, `backup_create/status/verify` (скачать и сверить хэш), `restore_plan` в изолированный staging.
- S4.K основа: база знаний с полным CRUD и историей ревизий (правка, слияние, замещение, архив, удаление с причиной), `knowledge_import_chat` — импорт экспортов ChatGPT/Claude/Markdown с provenance и маскированием секретов. Выгрузка статей в Google Drive документами — следующий срез.
- Описание — docs/STORAGE.md.
