# Failure classes

Ошибки executor и ответы MCP сохраняют исходную причину через `errors.Is/As`.
`errors.Is(err, executor.ErrHold)` остаётся совместимым. Тип
`internal/failure.Error` содержит `class`, `reason_code`, `site`, `message`,
`next_safe_action: {tool, arguments}`; контекст добавляет `budget_ms` и
известный `deadline`. Нулевой бюджет означает отсутствие известного остатка.

| Класс | Значение | Следующее безопасное действие по умолчанию |
| --- | --- | --- |
| QUOTA_EXHAUSTED | Лимит исчерпан | tool_resolve: quota status and reset time |
| DISCONNECTED | Backend недоступен | health_snapshot {} |
| OWNER_BUSY | Ресурс занят владельцем | tool_resolve: project_lease_status |
| AUTH_REQUIRED | Нет действующей аутентификации | admin_session_status {} |
| POLICY_DENIED | Вход или операция вне политики | tool_resolve: allowed read-only alternative |
| APPROVAL_REQUIRED | Требуется согласование | tool_resolve: approval status |
| FENCED_UNKNOWN | Admission, fencing или эффект не доказан | mutation_contracts {} |
| TRANSIENT | Операция прервана | tool_resolve: inspect interrupted operation before retry |
| FAILED_VERIFIED | Известна конкретная ошибка выполнения | tool_resolve: inspect verified failure |
| HOLD_EVIDENCE_MISSING | Нет требуемого доказательства | tool_resolve: integration_test_prepare |
| DEADLINE | Исчерпан бюджет контекста | tool_resolve: inspect deadline budget before retry |

`tool_resolve` получает указанный текст в аргументе `goal`.
Действие является предложением: классификация не выполняет ремонт, запуск,
смену владельца или повтор мутации. Параметры сессии и новый ключ вызова
подставляет вызывающий после проверки состояния операции.

При удалённом workspace GitVerse (`EVIDENCE_WORKSPACE_MISSING`) известный
permit сопоставляется с фиксированными попытками 1–9. Для попыток 1–8 действие
конкретизируется: `integration_test_prepare` с `path`, `commit`,
`evidence=gitverse` и `attempt+1`. Для неизвестного permit или попытки 9
остаётся поиск безопасного пути через `tool_resolve`; попытка не угадывается.

Типизированная причина имеет приоритет над эвристиками текста MCP.
Голый legacy HOLD получает `FENCED_UNKNOWN`, а не утверждение об отсутствии
изменений. В горячих путях коды различают проверку владельца/типа файла,
смену inode, конфликт поколения, отсутствие receipt, ошибку Git и deadline.
Числовые `cleanup plan check N` заменены кодами `CLEANUP_*`.

Регрессии: BUG-20261023-81 (deadline и бюджет Git), BUG-20261023-82
(утраченный workspace и следующая попытка GitVerse); существующие тесты
очистки сохраняют проверки BUG-20261024-89.
