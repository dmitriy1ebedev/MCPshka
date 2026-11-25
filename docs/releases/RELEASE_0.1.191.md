# MCPshka 0.1.191

Autopilot, этапы 2, 3, 4, 6, 7, 10.

- **project_pipeline** (этап 2, Codex): стадии prepare→build→test→lint→package→verify только по проверенному профилю; артефакты package из sandbox с SHA-256 и manifest (HEAD, tree, digest профиля); при неизменных входах PASS не перезапускается (reused). Профили Free My TV и DIAG — docs/PIPELINE.md.
- **approved_task** (этап 3): `approved_task_prepare` → ссылка Face ID на ровно этот план (исполняемый файл с SHA-256, argv без shell, workdir, пути записи, сеть, устройства, таймаут; страница показывает сгенерированную команду) → `approved_task_execute` один раз в песочнице systemd → `approved_task_status`. Grant коннектора обобщён в одноразовый Face ID-grant.
- **Strict acceptance** (этап 4): `acceptance_status path step` — машиночитаемая строка `Gates: …` под шагом ROADMAP, evidence привязано к HEAD/tree/версии/артефакту/конфигу/устройству, при изменении входа — STALE. `docker_smoke` и `project_verify` пишут gates docker/deploy. Пакет `internal/evidence`.
- **host_diagnose** (этап 6): ssh_drop / thread_eagain / hang / oom / slow → FACT → INFERENCE: load на ядро, память, 2-секундная выборка vmstat, OOM, потоки, pids и RLIMIT_NPROC сервисов, события ядра и sshd за 24 ч (брокер `health host_events`). Пакет `internal/hostdiag`.
- **Resource governor** (этап 7): тяжёлые задачи ждут (WAITING_RESOURCE), если мало памяти (<300 МиБ) или диска (<3 ГиБ) или идёт поток (отдача >1.5 МиБ/с; простаивающие сокеты go2rtc/ТВ не считаются). Юниты сборки — CPUWeight=20.
- **project_create** (этап 10): `/root/repos/<name>` через брокер, шаблон go/python/generic (README, ROADMAP с Gates, тест, Makefile verify), git init с первым коммитом, профиль. Пакет `internal/projectscaffold`.
