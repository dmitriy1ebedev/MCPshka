# MCPshka 0.1.173 — установка релиза без SSH владельца, DIAG внутри триажа

## Выпуск целиком через MCPshka
- **`release_apply` / `release_apply_status`** (Face ID): установка без `apply-release.sh` владельца. Root сверяет запечатанную квитанцию локального релиза (сборка + огороженный тест одного коммита), проверяет, что коммит — `main` защищённого хранилища и объявляет версию, перехеширует образы root, раскладывает кандидаты, хелпер и план из этого коммита, гоняет `coordinated-upgrade --check` и запускает `--apply` отдельным юнитом (он перезапускает MCPshka и сам откатывает оба бинарника при провале health).
- **`release_local` докачивает образы между вызовами** (BUG-20261023-66): первый выпуск без резервного bootstrap.

## DIAG
- **`host_triage` при нездоровом хосте прикладывает DIAG**: `diagnosis` — не-ok находки свежего расследования, или готовый `diag_investigation_start` (problems kind=diagnosis) с фиксированным ключом — один прогон на 10 минут, повторный триаж не плодит расследования. Триаж остаётся read-only.
- **`diag_compare`**: до/после двух прогонов по ключам находок — resolved/appeared/persisting и вердикт FIXED, IMPROVED, UNCHANGED, WORSE.
- **`diag_release_apply` / `diag_release_status`** (Face ID): новая DIAG на хост. Коммит только из `main` DIAG, распаковка `git archive` только обычных файлов, офлайн-сборка diag и diagd с версией из `version.go`, smoke нового diag от nobody, замена с `.previous`, перезапуск diagd и откат, если установленная пара не отвечает. Старый путь через хелперы `mcpshka-diag-*` (их на хосте нет) не использовался.
- **`project_build` берёт workflow `build` из профиля проекта** раньше угадывания по маркерам (BUG-20261023-69: Makefile DIAG давал `make`, а он разрешён только в изолированном тесте).

## Исправлено
- BUG-20261023-73: доказательство GitVerse не готовилось никогда с 0.1.172 — первая проверка подготовки знала только метку `ci:github`, а не `ci:gitverse`.
- BUG-20261023-70: однозначный отказ обработчика (например, `fs_write` без родительского каталога) оставлял квитанцию UNKNOWN с `retry_allowed=false` — агент вставал без пути. Теперь `mutation_probe` отдаёт `handler_failed`, `retry_allowed=true` (новым ключом), если ошибка не FENCED_UNKNOWN.
- BUG-20261023-64: классы ошибок по целым словам («release» больше не «lease» → OWNER_BUSY).
- BUG-20261023-65: у `release_build_prepare` своя схема.
- Офлайн-образ CI для MosHub собирается buildah (на раннерах GitVerse нет docker-демона); GigaIDE lane (packaging/gigaide); темы справки release и agents.
