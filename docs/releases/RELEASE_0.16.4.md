# MCPshka 0.16.4 — решения владельца 18.11

Подготовлен код; production остаётся 0.16.3. Выпуск выполняет владелец чата.

- Distributed CI распределяет четыре первичных шарда между GitHub, GitVerse,
  MosHub и SourceCraft. SourceCraft публикует ранний source-bound статус и
  итоговый report.json; очередь офлайн-площадки перераздаётся, CODE_FAIL
  не скрывается повтором. H96 остаётся последним резервом для MCPshka.
- DIAG/Lane получают external-only профили и `forge_ci provider=distributed
  path=...`: тесты и ARM64-сборки на площадках, готовые артефакты скачиваются
  с проверкой размера/SHA-256. Installer хеширует их повторно. Legacy DIAG
  release больше не компилирует на H96; recipe-based release требует
  CI-артефакты и отклоняет локальные компиляторы/интерпретаторы.
- llm.ask/compare/models в code_run возвращают bounded job. Ответы читаются
  через job_wait/job_output, клиентская отмена не теряет готовящийся ответ.
  Явный job_cancel завершает HTTP-запрос; перезапуск позволяет новый запрос.
- Отмена code_run освобождает BUSY, включая медленный вложенный read-only tool;
  число ещё завершающихся вызовов ограничено.
- project_release читает свойства systemd по именам и показывает RUNNING
  при activating. UNKNOWN запроса не превращается в ложный FAILED.
- Self-heal сбрасывает failed у доказанно своих idle transient-юнитов.
  Docker scope требует привязки к allowlist контейнеров и отсутствия cgroup.
  До reset-failed записывается намерение в системный журнал, затем результат.

Проверки: project_test по затронутым пакетам; regression/contract tests для
разбиения, перераздачи, SourceCraft report identity, CI-файлов, artifact SHA,
фоновых jobs/cancel/restart, состояния release и границ transient self-heal.
Живая четырёхплощадочная приёмка DIAG/Lane требует публикации закреплённого
worker commit и CI-конфигураций в приватные репозитории. Смена рецептов
установки остаётся в штатном процессе approval. Прод не обновлялся.
