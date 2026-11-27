# MCPshka 0.1.193

Всё недоделанное за 25.10 одним релизом.

- **Mission Controller** (этап 9, Codex): `mission_preflight/start/status/resume/pause/cancel/report`, режимы CLOSE_STEP/TIER/ROADMAP/REPAIR_AND_RELEASE/DIAGNOSE_AND_RECOVER/AUDIT_ONLY; миссия хранится в SQLite, resume — с последнего доказанного checkpoint (acceptance_status), preflight = capability_preflight + acceptance до правки исходников; агенты — только с флагом agents_allowed. project_pipeline вынесен в `internal/pipeline`, gates — в общий evidence.
- **project_commit**: коммит ровно перечисленных файлов под lease (без хуков, чужие staged-пути не попадают) — для клиентов, которые блокируют сырой git (DIAG, Lane в ChatGPT). Справка tests: правки → project_commit → project_test на новом HEAD.
- **Профиль test может быть `./tests/script`**: sealed-тест запускает закоммиченный скрипт из снимка; Lane больше не упирается в запрет `sh`.
- **webos_deploy для любого IPK**: id и версия из control самого IPK (go2rtc PiP-оверлей `io.freemytv.cameraoverlay`).
- **webos_test** (этап 11): без изменений на ТВ — порт Developer Mode, установленная версия, запущено ли приложение, доходит ли поток go2rtc до ТВ и с каким битрейтом.
- **media_stream**: без fps (API go2rtc не принимает источники с пробелами).
- **BUG-111**: integration_ship не висит на recovery брокера — recovery идёт в фоне, ship отвечает WORKER_RECONCILING.
- **BUG-110**: уборка хоста при исчерпании бюджета — DEFERRED, а не упавший юнит.
