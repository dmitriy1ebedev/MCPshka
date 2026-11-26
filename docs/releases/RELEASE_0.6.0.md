# MCPshka 0.6.0 — гибкая перегрузка и запасной CI

Дата: 2026-11-04. База: main 94515da (0.5.1).

## Изменено
- **BUG-20261104-35, хост.** `internal/hostload`: процессы в состоянии R (без читающего) сглажены EWMA с τ=30 с; занято с 1.5/ядро, свободно ниже 1.2/ядро, всегда занято при load1 > 3/ядро. PSI на ядре H96 нет, поэтому runnable вместо load1 (load1 включает D-state). Используют heavyqueue, локальный выпуск и host_triage.
- **BUG-20261104-35, broker.** `brokerclient`: адаптивное окно (AIMD, 8→16, ×½ на медленный ответ, +1/limit на успех). Превышение срока чтения после записи — «медленно», а не «упал»: цепь не размыкается. Нет места до срока — `BROKER_BUSY`, запрос не отправлен. Срок по умолчанию 35 с × clamp(load1/ядро, 1, 2).

## Добавлено
- `integration_ship evidence=ci`: публикация снимка на GitHub, PR в main (переиспользуется), ожидание прогона; `CI_RUNNING`/`CI_FAILED`. `forge_ci provider=github mode=pr`.
- `forge_ci provider=moshub mode=rerun` — POST /pipelines/{id}/retry.
- Face ID админ-сессии: истёкший вызов переиздаётся на месте того же поколения (не отменён, не активен, UV обязателен).

## Исправлено
- **BUG-20261104-36.** `gitops.Commit` отказывает в коммите в main `/root/repos/mcpshka`; READY head mismatch называет «target moved outside integration».
- **BUG-20261104-37.** Ключ шага ship: `ship:<id>:<commit>:<attempt>:<step>`; отказ worker_recover несёт вывод воркера.

- **BUG-20261104-38.** `packaging/gitverse/ci_status.py`: частичный листинг строго «новые первыми» с найденным прогоном снимка считается полным, листание останавливается на нём; неупорядоченный — HOLD.

## Откат
`release_apply` на 0.5.1 (предыдущий выпуск сохраняется установщиком).
