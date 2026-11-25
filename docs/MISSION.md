# Mission Controller

Миссия — запись в SQLite MCPshka, независимая от чата. Она хранит цель,
упорядоченный план, состояние, историю, блокеры и последний доказанный checkpoint.
Контроллер не запускает фонового исполнителя и не получает административных прав.

- `mission_preflight`: для каждого шага вызывает `capability_preflight` и
  `acceptance_status`; возвращает блокер с `next_safe_action`.
- `mission_start`: сохраняет проверенный план. Нужны `path`, `goal`, `mode`,
  `steps: [{id, capabilities?}]` и `idempotency_key`. Для одного шага достаточно
  `step`. Для `TIER` нужен также `tier`; план tier/roadmap задаёт вызывающий ИИ,
  чтобы границы работы были явными. Неотмеченные критерии не считаются PASS.
- `mission_status id=…`: читает миссию и свежую приёмку; без `id` сохраняет
  прежний обзор задач, интеграций и leases, а также возвращает `missions` —
  ограниченный индекс ID и сохранённых состояний. Это подсказки для восстановления
  после потери чата, не свежие доказательства приёмки; есть `missions_truncated`.
- `mission_resume`: перепроверяет сохранённые checkpoints; изменившиеся
  HEAD/tree или потерянный PASS возвращают к первому недоказанному шагу.
  Один вызов может доказать только текущий шаг. Нужен новый ключ для продвижения;
  повтор старого ключа возвращает исходный ответ.
- `mission_pause` / `mission_cancel`: меняют состояние миссии, не убивают job
  и не повторяют неизвестные операции. Отменённую миссию возобновить нельзя.
- `mission_report`: Mission · Current step · State · Version · Git HEAD ·
  Passed gates · Pending · Production · Active job · Next action · User action required.

Режимы: `CLOSE_STEP`, `TIER`, `ROADMAP`, `REPAIR_AND_RELEASE`,
`DIAGNOSE_AND_RECOVER`, `AUDIT_ONLY`. Они задают стандартный набор возможностей
и следующий безопасный шаг; явный `capabilities` уточняет зависимости шага.
В `AUDIT_ONLY` контроллер выдаёт только действия чтения.

Исполнение ведёт вызывающий ИИ существующими `workflow_*`, `project_pipeline`,
`phase_gate`, `integration_ship`, `release_*` и профильными инструментами.
`agents_allowed` по умолчанию `false`: `agent_task` и `roadmap_run` не предлагаются.
При явном согласии (`true`) следующий шаг может быть передан `agent_task`;
для его фактического запуска всё равно требуется штатное окно Face ID.
Согласие не разрешает root-shell, deployment или обход recovery.

Checkpoint создаётся только из свежего `acceptance_status`: все gates PASS,
есть HEAD/tree и чистое рабочее дерево. Успешный job или `phase_gate` сами по
себе не закрывают шаг. Нет доказательства — блокер или следующий шаг, не DONE.
Пауза/отмена сохраняются; чтение статуса не создаёт checkpoints. Production
и active jobs берутся из `project_context`; недоступные сведения — UNKNOWN.
